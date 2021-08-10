package ksql

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/pkg/errors"
	"github.com/vingarcia/ksql/kstructs"
)

var selectQueryCache = map[string]map[reflect.Type]string{}

func init() {
	for dname := range supportedDialects {
		selectQueryCache[dname] = map[reflect.Type]string{}
	}
}

// DB represents the ksql client responsible for
// interfacing with the "database/sql" package implementing
// the KissSQL interface `ksql.Provider`.
type DB struct {
	driver  string
	dialect Dialect
	db      DBAdapter
}

// DBAdapter is minimalistic interface to decouple our implementation
// from database/sql, i.e. if any struct implements the functions below
// with the exact same semantic as the sql package it will work with ksql.
//
// To create a new client using this adapter use ksql.NewWithDB()
type DBAdapter interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (Result, error)
	QueryContext(ctx context.Context, query string, args ...interface{}) (Rows, error)
}

// TxBeginner needs to be implemented by the DBAdapter in order to make it possible
// to use the `ksql.Transaction()` function.
type TxBeginner interface {
	BeginTx(ctx context.Context) (Tx, error)
}

// Result stores information about the result of an Exec query
type Result interface {
	LastInsertId() (int64, error)
	RowsAffected() (int64, error)
}

// Rows represents the results from a call to Query()
type Rows interface {
	Scan(...interface{}) error
	Close() error
	Next() bool
	Err() error
	Columns() ([]string, error)
}

// Tx represents a transaction and is expected to be returned by the DBAdapter.BeginTx function
type Tx interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (Result, error)
	QueryContext(ctx context.Context, query string, args ...interface{}) (Rows, error)
	Rollback(ctx context.Context) error
	Commit(ctx context.Context) error
}

// Config describes the optional arguments accepted
// by the ksql.New() function.
type Config struct {
	// MaxOpenCons defaults to 1 if not set
	MaxOpenConns int
}

// New instantiates a new KissSQL client
func New(
	dbDriver string,
	connectionString string,
	config Config,
) (DB, error) {
	db, err := sql.Open(dbDriver, connectionString)
	if err != nil {
		return DB{}, err
	}
	if err = db.Ping(); err != nil {
		return DB{}, err
	}

	if config.MaxOpenConns == 0 {
		config.MaxOpenConns = 1
	}

	db.SetMaxOpenConns(config.MaxOpenConns)

	return NewWithAdapter(SQLAdapter{db}, dbDriver, connectionString)
}

// NewWithPGX instantiates a new KissSQL client using the pgx
// library in the backend
func NewWithPGX(
	ctx context.Context,
	connectionString string,
	config Config,
) (db DB, err error) {
	pgxConf, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return DB{}, err
	}

	pgxConf.MaxConns = int32(config.MaxOpenConns)

	pool, err := pgxpool.ConnectConfig(ctx, pgxConf)
	if err != nil {
		return DB{}, err
	}
	if err = pool.Ping(ctx); err != nil {
		return DB{}, err
	}

	db, err = NewWithAdapter(PGXAdapter{pool}, "postgres", connectionString)
	return db, err
}

// NewWithAdapter allows the user to insert a custom implementation
// of the DBAdapter interface
func NewWithAdapter(
	db DBAdapter,
	dbDriver string,
	connectionString string,
) (DB, error) {
	dialect := supportedDialects[dbDriver]
	if dialect == nil {
		return DB{}, fmt.Errorf("unsupported driver `%s`", dbDriver)
	}

	return DB{
		dialect: dialect,
		driver:  dbDriver,
		db:      db,
	}, nil
}

// Query queries several rows from the database,
// the input should be a slice of structs (or *struct) passed
// by reference and it will be filled with all the results.
//
// Note: it is very important to make sure the query will
// return a small known number of results, otherwise you risk
// of overloading the available memory.
func (c DB) Query(
	ctx context.Context,
	records interface{},
	query string,
	params ...interface{},
) error {
	slicePtr := reflect.ValueOf(records)
	slicePtrType := slicePtr.Type()
	if slicePtrType.Kind() != reflect.Ptr {
		return fmt.Errorf("ksql: expected to receive a pointer to slice of structs, but got: %T", records)
	}
	sliceType := slicePtrType.Elem()
	slice := slicePtr.Elem()
	structType, isSliceOfPtrs, err := kstructs.DecodeAsSliceOfStructs(sliceType)
	if err != nil {
		return err
	}

	if isSliceOfPtrs {
		// Truncate the slice so there is no risk
		// of overwritting records that were already saved
		// on the slice:
		slice = slice.Slice(0, 0)
	}

	info := kstructs.GetTagInfo(structType)

	firstToken := strings.ToUpper(getFirstToken(query))
	if info.IsNestedStruct && firstToken == "SELECT" {
		// This error check is necessary, since if we can't build the select part of the query this feature won't work.
		return fmt.Errorf("can't generate SELECT query for nested struct: when using this feature omit the SELECT part of the query")
	}

	if firstToken == "FROM" {
		selectPrefix, err := buildSelectQuery(c.dialect, structType, info, selectQueryCache[c.dialect.DriverName()])
		if err != nil {
			return err
		}
		query = selectPrefix + query
	}

	rows, err := c.db.QueryContext(ctx, query, params...)
	if err != nil {
		return fmt.Errorf("error running query: %s", err.Error())
	}
	defer rows.Close()

	for idx := 0; rows.Next(); idx++ {
		// Allocate new slice elements
		// only if they are not already allocated:
		if slice.Len() <= idx {
			var elemValue reflect.Value
			elemValue = reflect.New(structType)
			if !isSliceOfPtrs {
				elemValue = elemValue.Elem()
			}
			slice = reflect.Append(slice, elemValue)
		}

		elemPtr := slice.Index(idx).Addr()
		if isSliceOfPtrs {
			// This is necessary since scanRows expects a *record not a **record
			elemPtr = elemPtr.Elem()
		}

		err = scanRows(c.dialect, rows, elemPtr.Interface())
		if err != nil {
			return err
		}
	}

	if err := rows.Close(); err != nil {
		return err
	}

	if rows.Err() != nil {
		return rows.Err()
	}

	// Update the original slice passed by reference:
	slicePtr.Elem().Set(slice)

	return nil
}

// QueryOne queries one instance from the database,
// the input struct must be passed by reference
// and the query should return only one result.
//
// QueryOne returns a ErrRecordNotFound if
// the query returns no results.
func (c DB) QueryOne(
	ctx context.Context,
	record interface{},
	query string,
	params ...interface{},
) error {
	t := reflect.TypeOf(record)
	if t.Kind() != reflect.Ptr {
		return fmt.Errorf("ksql: expected to receive a pointer to struct, but got: %T", record)
	}
	t = t.Elem()
	if t.Kind() != reflect.Struct {
		return fmt.Errorf("ksql: expected to receive a pointer to struct, but got: %T", record)
	}

	info := kstructs.GetTagInfo(t)

	firstToken := strings.ToUpper(getFirstToken(query))
	if info.IsNestedStruct && firstToken == "SELECT" {
		// This error check is necessary, since if we can't build the select part of the query this feature won't work.
		return fmt.Errorf("can't generate SELECT query for nested struct: when using this feature omit the SELECT part of the query")
	}

	if firstToken == "FROM" {
		selectPrefix, err := buildSelectQuery(c.dialect, t, info, selectQueryCache[c.dialect.DriverName()])
		if err != nil {
			return err
		}
		query = selectPrefix + query
	}

	rows, err := c.db.QueryContext(ctx, query, params...)
	if err != nil {
		return err
	}
	defer rows.Close()

	if !rows.Next() {
		if rows.Err() != nil {
			return rows.Err()
		}
		return ErrRecordNotFound
	}

	err = scanRows(c.dialect, rows, record)
	if err != nil {
		return err
	}

	return rows.Close()
}

// QueryChunks is meant to perform queries that returns
// more results than would normally fit on memory,
// for others cases the Query and QueryOne functions are indicated.
//
// The ChunkParser argument has 4 attributes:
// (1) The Query;
// (2) The query args;
// (3) The chunk size;
// (4) A callback function called ForEachChunk, that will be called
// to process each chunk loaded from the database.
//
// Note that the signature of the ForEachChunk callback can be
// any function that receives a slice of structs or a slice of
// pointers to struct as its only argument and that reflection
// will be used to instantiate this argument and to fill it
// with the database rows.
func (c DB) QueryChunks(
	ctx context.Context,
	parser ChunkParser,
) error {
	fnValue := reflect.ValueOf(parser.ForEachChunk)
	chunkType, err := kstructs.ParseInputFunc(parser.ForEachChunk)
	if err != nil {
		return err
	}

	chunk := reflect.MakeSlice(chunkType, 0, parser.ChunkSize)

	structType, isSliceOfPtrs, err := kstructs.DecodeAsSliceOfStructs(chunkType)
	if err != nil {
		return err
	}

	info := kstructs.GetTagInfo(structType)

	firstToken := strings.ToUpper(getFirstToken(parser.Query))
	if info.IsNestedStruct && firstToken == "SELECT" {
		// This error check is necessary, since if we can't build the select part of the query this feature won't work.
		return fmt.Errorf("can't generate SELECT query for nested struct: when using this feature omit the SELECT part of the query")
	}

	if firstToken == "FROM" {
		selectPrefix, err := buildSelectQuery(c.dialect, structType, info, selectQueryCache[c.dialect.DriverName()])
		if err != nil {
			return err
		}
		parser.Query = selectPrefix + parser.Query
	}

	rows, err := c.db.QueryContext(ctx, parser.Query, parser.Params...)
	if err != nil {
		return err
	}
	defer rows.Close()

	var idx = 0
	for rows.Next() {
		// Allocate new slice elements
		// only if they are not already allocated:
		if chunk.Len() <= idx {
			var elemValue reflect.Value
			elemValue = reflect.New(structType)
			if !isSliceOfPtrs {
				elemValue = elemValue.Elem()
			}
			chunk = reflect.Append(chunk, elemValue)
		}

		err = scanRows(c.dialect, rows, chunk.Index(idx).Addr().Interface())
		if err != nil {
			return err
		}

		if idx < parser.ChunkSize-1 {
			idx++
			continue
		}

		idx = 0
		err, _ = fnValue.Call([]reflect.Value{chunk})[0].Interface().(error)
		if err != nil {
			if err == ErrAbortIteration {
				return nil
			}
			return err
		}
	}

	if err := rows.Close(); err != nil {
		return err
	}

	// If Next() returned false because of an error:
	if rows.Err() != nil {
		return rows.Err()
	}

	// If no rows were found or idx was reset to 0
	// on the last iteration skip this last call to ForEachChunk:
	if idx > 0 {
		chunk = chunk.Slice(0, idx)

		err, _ = fnValue.Call([]reflect.Value{chunk})[0].Interface().(error)
		if err != nil {
			if err == ErrAbortIteration {
				return nil
			}
			return err
		}
	}

	return nil
}

// Insert one or more instances on the database
//
// If the original instances have been passed by reference
// the ID is automatically updated after insertion is completed.
func (c DB) Insert(
	ctx context.Context,
	table Table,
	record interface{},
) error {
	query, params, scanValues, err := buildInsertQuery(c.dialect, table.name, record, table.idColumns...)
	if err != nil {
		return err
	}

	switch table.insertMethodFor(c.dialect) {
	case insertWithReturning, insertWithOutput:
		err = c.insertReturningIDs(ctx, record, query, params, scanValues, table.idColumns)
	case insertWithLastInsertID:
		err = c.insertWithLastInsertID(ctx, record, query, params, table.idColumns[0])
	case insertWithNoIDRetrieval:
		err = c.insertWithNoIDRetrieval(ctx, record, query, params)
	default:
		// Unsupported drivers should be detected on the New() function,
		// So we don't expect the code to ever get into this default case.
		err = fmt.Errorf("code error: unsupported driver `%s`", c.driver)
	}

	return err
}

func (c DB) insertReturningIDs(
	ctx context.Context,
	record interface{},
	query string,
	params []interface{},
	scanValues []interface{},
	idNames []string,
) error {
	rows, err := c.db.QueryContext(ctx, query, params...)
	if err != nil {
		return err
	}
	defer rows.Close()

	if !rows.Next() {
		err := fmt.Errorf("unexpected error retrieving the id columns from the database")
		if rows.Err() != nil {
			err = rows.Err()
		}

		return err
	}

	err = rows.Scan(scanValues...)
	if err != nil {
		return err
	}

	return rows.Close()
}

func (c DB) insertWithLastInsertID(
	ctx context.Context,
	record interface{},
	query string,
	params []interface{},
	idName string,
) error {
	result, err := c.db.ExecContext(ctx, query, params...)
	if err != nil {
		return err
	}

	v := reflect.ValueOf(record)
	t := v.Type()
	if err = assertStructPtr(t); err != nil {
		return errors.Wrap(err, "can't write to `"+idName+"` field")
	}

	info := kstructs.GetTagInfo(t.Elem())

	id, err := result.LastInsertId()
	if err != nil {
		return err
	}

	vID := reflect.ValueOf(id)
	tID := vID.Type()

	fieldAddr := v.Elem().Field(info.ByName(idName).Index).Addr()
	fieldType := fieldAddr.Type().Elem()

	if !tID.ConvertibleTo(fieldType) {
		return fmt.Errorf(
			"Can't convert last insert id of type int64 into field `%s` of type %s",
			idName,
			fieldType,
		)
	}

	fieldAddr.Elem().Set(vID.Convert(fieldType))
	return nil
}

func (c DB) insertWithNoIDRetrieval(
	ctx context.Context,
	record interface{},
	query string,
	params []interface{},
) error {
	_, err := c.db.ExecContext(ctx, query, params...)
	return err
}

func assertStructPtr(t reflect.Type) error {
	if t.Kind() != reflect.Ptr {
		return fmt.Errorf("expected a Kind of Ptr but got: %s", t)
	}
	if t.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("expected a Kind of Ptr to Struct but got: %s", t)
	}
	return nil
}

// Delete deletes one or more instances from the database by id
func (c DB) Delete(
	ctx context.Context,
	table Table,
	ids ...interface{},
) error {
	if len(ids) == 0 {
		return nil
	}

	idMaps, err := normalizeIDsAsMaps(table.idColumns, ids)
	if err != nil {
		return err
	}

	var query string
	var params []interface{}
	if len(table.idColumns) == 1 {
		query, params = buildSingleKeyDeleteQuery(c.dialect, table.name, table.idColumns[0], idMaps)
	} else {
		query, params = buildCompositeKeyDeleteQuery(c.dialect, table.name, table.idColumns, idMaps)
	}

	_, err = c.db.ExecContext(ctx, query, params...)

	return err
}

func normalizeIDsAsMaps(idNames []string, ids []interface{}) ([]map[string]interface{}, error) {
	if len(idNames) == 0 {
		return nil, fmt.Errorf("internal ksql error: missing idNames")
	}

	idMaps := []map[string]interface{}{}
	for i := range ids {
		t := reflect.TypeOf(ids[i])
		switch t.Kind() {
		case reflect.Struct:
			m, err := kstructs.StructToMap(ids[i])
			if err != nil {
				return nil, errors.Wrapf(err, "could not get ID(s) from record on idx %d", i)
			}
			idMaps = append(idMaps, m)
		case reflect.Map:
			m, ok := ids[i].(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("expected map[string]interface{} but got %T", ids[i])
			}
			idMaps = append(idMaps, m)
		default:
			idMaps = append(idMaps, map[string]interface{}{
				idNames[0]: ids[i],
			})
		}
	}

	for i, m := range idMaps {
		for _, id := range idNames {
			if _, found := m[id]; !found {
				return nil, fmt.Errorf("missing required id field `%s` on record with idx %d", id, i)
			}
		}
	}

	return idMaps, nil
}

// Update updates the given instances on the database by id.
//
// Partial updates are supported, i.e. it will ignore nil pointer attributes
func (c DB) Update(
	ctx context.Context,
	table Table,
	record interface{},
) error {
	query, params, err := buildUpdateQuery(c.dialect, table.name, record, table.idColumns...)
	if err != nil {
		return err
	}

	result, err := c.db.ExecContext(ctx, query, params...)
	if err != nil {
		return err
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf(
			"unexpected error: unable to fetch how many rows were affected by the update: %s",
			err,
		)
	}
	if n < 1 {
		return ErrRecordNotFound
	}

	return nil
}

func buildInsertQuery(
	dialect Dialect,
	tableName string,
	record interface{},
	idNames ...string,
) (query string, params []interface{}, scanValues []interface{}, err error) {
	v := reflect.ValueOf(record)
	t := v.Type()
	if err = assertStructPtr(t); err != nil {
		return "", nil, nil, fmt.Errorf(
			"ksql: expected record to be a pointer to struct, but got: %T",
			record,
		)
	}

	info := kstructs.GetTagInfo(t.Elem())

	recordMap, err := kstructs.StructToMap(record)
	if err != nil {
		return "", nil, nil, err
	}

	for _, fieldName := range idNames {
		field, found := recordMap[fieldName]
		if !found {
			continue
		}

		// Remove any ID field that was not set:
		if reflect.ValueOf(field).IsZero() {
			delete(recordMap, fieldName)
		}
	}

	columnNames := []string{}
	for col := range recordMap {
		columnNames = append(columnNames, col)
	}

	params = make([]interface{}, len(recordMap))
	valuesQuery := make([]string, len(recordMap))
	for i, col := range columnNames {
		recordValue := recordMap[col]
		params[i] = recordValue
		if info.ByName(col).SerializeAsJSON {
			params[i] = jsonSerializable{
				DriverName: dialect.DriverName(),
				Attr:       recordValue,
			}
		}

		valuesQuery[i] = dialect.Placeholder(i)
	}

	// Escape all cols to be sure they will be interpreted as column names:
	escapedColumnNames := []string{}
	for _, col := range columnNames {
		escapedColumnNames = append(escapedColumnNames, dialect.Escape(col))
	}

	var returningQuery, outputQuery string
	switch dialect.InsertMethod() {
	case insertWithReturning:
		escapedIDNames := []string{}
		for _, id := range idNames {
			escapedIDNames = append(escapedIDNames, dialect.Escape(id))
		}
		returningQuery = " RETURNING " + strings.Join(escapedIDNames, ", ")

		for _, id := range idNames {
			scanValues = append(
				scanValues,
				v.Elem().Field(info.ByName(id).Index).Addr().Interface(),
			)
		}
	case insertWithOutput:
		escapedIDNames := []string{}
		for _, id := range idNames {
			escapedIDNames = append(escapedIDNames, "INSERTED."+dialect.Escape(id))
		}
		outputQuery = " OUTPUT " + strings.Join(escapedIDNames, ", ")

		for _, id := range idNames {
			scanValues = append(
				scanValues,
				v.Elem().Field(info.ByName(id).Index).Addr().Interface(),
			)
		}
	}

	// Note that the outputQuery and the returningQuery depend
	// on the selected driver, thus, they might be empty strings.
	query = fmt.Sprintf(
		"INSERT INTO %s (%s)%s VALUES (%s)%s",
		dialect.Escape(tableName),
		strings.Join(escapedColumnNames, ", "),
		outputQuery,
		strings.Join(valuesQuery, ", "),
		returningQuery,
	)

	return query, params, scanValues, nil
}

func buildUpdateQuery(
	dialect Dialect,
	tableName string,
	record interface{},
	idFieldNames ...string,
) (query string, args []interface{}, err error) {
	recordMap, err := kstructs.StructToMap(record)
	if err != nil {
		return "", nil, err
	}
	numAttrs := len(recordMap)
	args = make([]interface{}, numAttrs)
	numNonIDArgs := numAttrs - len(idFieldNames)
	whereArgs := args[numNonIDArgs:]

	whereQuery := make([]string, len(idFieldNames))
	for i, fieldName := range idFieldNames {
		whereArgs[i] = recordMap[fieldName]
		whereQuery[i] = fmt.Sprintf(
			"%s = %s",
			dialect.Escape(fieldName),
			dialect.Placeholder(i+numNonIDArgs),
		)

		delete(recordMap, fieldName)
	}

	keys := []string{}
	for key := range recordMap {
		keys = append(keys, key)
	}

	t := reflect.TypeOf(record)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	info := kstructs.GetTagInfo(t)

	var setQuery []string
	for i, k := range keys {
		recordValue := recordMap[k]
		if info.ByName(k).SerializeAsJSON {
			recordValue = jsonSerializable{
				DriverName: dialect.DriverName(),
				Attr:       recordValue,
			}
		}
		args[i] = recordValue
		setQuery = append(setQuery, fmt.Sprintf(
			"%s = %s",
			dialect.Escape(k),
			dialect.Placeholder(i),
		))
	}

	query = fmt.Sprintf(
		"UPDATE %s SET %s WHERE %s",
		dialect.Escape(tableName),
		strings.Join(setQuery, ", "),
		strings.Join(whereQuery, ", "),
	)

	return query, args, nil
}

// Exec just runs an SQL command on the database returning no rows.
func (c DB) Exec(ctx context.Context, query string, params ...interface{}) error {
	_, err := c.db.ExecContext(ctx, query, params...)
	return err
}

// Transaction just runs an SQL command on the database returning no rows.
func (c DB) Transaction(ctx context.Context, fn func(Provider) error) error {
	switch txBeginner := c.db.(type) {
	case Tx:
		return fn(c)
	case TxBeginner:
		tx, err := txBeginner.BeginTx(ctx)
		if err != nil {
			return err
		}
		defer func() {
			if r := recover(); r != nil {
				rollbackErr := tx.Rollback(ctx)
				if rollbackErr != nil {
					r = errors.Wrap(rollbackErr,
						fmt.Sprintf("unable to rollback after panic with value: %v", r),
					)
				}
				panic(r)
			}
		}()

		ormCopy := c
		ormCopy.db = tx

		err = fn(ormCopy)
		if err != nil {
			rollbackErr := tx.Rollback(ctx)
			if rollbackErr != nil {
				err = errors.Wrap(rollbackErr,
					fmt.Sprintf("unable to rollback after error: %s", err.Error()),
				)
			}
			return err
		}

		return tx.Commit(ctx)

	default:
		return fmt.Errorf("can't start transaction: The DBAdapter doesn't implement the TxBegginner interface")
	}
}

type nopScanner struct{}

var nopScannerValue = reflect.ValueOf(&nopScanner{}).Interface()

func (nopScanner) Scan(value interface{}) error {
	return nil
}

func scanRows(dialect Dialect, rows Rows, record interface{}) error {
	v := reflect.ValueOf(record)
	t := v.Type()
	if t.Kind() != reflect.Ptr {
		return fmt.Errorf("ksql: expected record to be a pointer to struct, but got: %T", record)
	}

	v = v.Elem()
	t = t.Elem()

	if t.Kind() != reflect.Struct {
		return fmt.Errorf("ksql: expected record to be a pointer to struct, but got: %T", record)
	}

	info := kstructs.GetTagInfo(t)

	var scanArgs []interface{}
	if info.IsNestedStruct {
		// This version is positional meaning that it expect the arguments
		// to follow an specific order. It's ok because we don't allow the
		// user to type the "SELECT" part of the query for nested kstructs.
		scanArgs = getScanArgsForNestedStructs(dialect, rows, t, v, info)
	} else {
		names, err := rows.Columns()
		if err != nil {
			return err
		}
		// Since this version uses the names of the columns it works
		// with any order of attributes/columns.
		scanArgs = getScanArgsFromNames(dialect, names, v, info)
	}

	return rows.Scan(scanArgs...)
}

func getScanArgsForNestedStructs(dialect Dialect, rows Rows, t reflect.Type, v reflect.Value, info kstructs.StructInfo) []interface{} {
	scanArgs := []interface{}{}
	for i := 0; i < v.NumField(); i++ {
		// TODO(vingarcia00): Handle case where type is pointer
		nestedStructInfo := kstructs.GetTagInfo(t.Field(i).Type)
		nestedStructValue := v.Field(i)
		for j := 0; j < nestedStructValue.NumField(); j++ {
			fieldInfo := nestedStructInfo.ByIndex(j)

			valueScanner := nopScannerValue
			if fieldInfo.Valid {
				valueScanner = nestedStructValue.Field(fieldInfo.Index).Addr().Interface()
				if fieldInfo.SerializeAsJSON {
					valueScanner = &jsonSerializable{
						DriverName: dialect.DriverName(),
						Attr:       valueScanner,
					}
				}
			}

			scanArgs = append(scanArgs, valueScanner)
		}
	}

	return scanArgs
}

func getScanArgsFromNames(dialect Dialect, names []string, v reflect.Value, info kstructs.StructInfo) []interface{} {
	scanArgs := []interface{}{}
	for _, name := range names {
		fieldInfo := info.ByName(name)

		valueScanner := nopScannerValue
		if fieldInfo.Valid {
			valueScanner = v.Field(fieldInfo.Index).Addr().Interface()
			if fieldInfo.SerializeAsJSON {
				valueScanner = &jsonSerializable{
					DriverName: dialect.DriverName(),
					Attr:       valueScanner,
				}
			}
		}

		scanArgs = append(scanArgs, valueScanner)
	}

	return scanArgs
}

func buildSingleKeyDeleteQuery(
	dialect Dialect,
	table string,
	idName string,
	idMaps []map[string]interface{},
) (query string, params []interface{}) {
	values := []string{}
	for i, m := range idMaps {
		values = append(values, dialect.Placeholder(i))
		params = append(params, m[idName])
	}

	return fmt.Sprintf(
		"DELETE FROM %s WHERE %s IN (%s)",
		dialect.Escape(table),
		dialect.Escape(idName),
		strings.Join(values, ","),
	), params
}

func buildCompositeKeyDeleteQuery(
	dialect Dialect,
	table string,
	idNames []string,
	idMaps []map[string]interface{},
) (query string, params []interface{}) {
	escapedNames := []string{}
	for _, name := range idNames {
		escapedNames = append(escapedNames, dialect.Escape(name))
	}

	values := []string{}
	for _, m := range idMaps {
		tuple := []string{}
		for _, name := range idNames {
			params = append(params, m[name])
			tuple = append(tuple, dialect.Placeholder(len(values)))
		}
		values = append(values, "("+strings.Join(tuple, ",")+")")
	}

	return fmt.Sprintf(
		"DELETE FROM %s WHERE (%s) IN (VALUES %s)",
		dialect.Escape(table),
		strings.Join(escapedNames, ","),
		strings.Join(values, ","),
	), params
}

// We implemented this function instead of using
// a regex or strings.Fields because we wanted
// to preserve the performance of the package.
func getFirstToken(s string) string {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)

	var token strings.Builder
	for _, c := range s {
		if unicode.IsSpace(c) {
			break
		}
		token.WriteRune(c)
	}
	return token.String()
}

func buildSelectQuery(
	dialect Dialect,
	structType reflect.Type,
	info kstructs.StructInfo,
	selectQueryCache map[reflect.Type]string,
) (query string, err error) {
	if selectQuery, found := selectQueryCache[structType]; found {
		return selectQuery, nil
	}

	if info.IsNestedStruct {
		query, err = buildSelectQueryForNestedStructs(dialect, structType, info)
		if err != nil {
			return "", err
		}
	} else {
		query = buildSelectQueryForPlainStructs(dialect, structType, info)
	}

	selectQueryCache[structType] = query
	return query, nil
}

func buildSelectQueryForPlainStructs(
	dialect Dialect,
	structType reflect.Type,
	info kstructs.StructInfo,
) string {
	var fields []string
	for i := 0; i < structType.NumField(); i++ {
		fields = append(fields, dialect.Escape(info.ByIndex(i).Name))
	}

	return "SELECT " + strings.Join(fields, ", ") + " "
}

func buildSelectQueryForNestedStructs(
	dialect Dialect,
	structType reflect.Type,
	info kstructs.StructInfo,
) (string, error) {
	var fields []string
	for i := 0; i < structType.NumField(); i++ {
		nestedStructName := info.ByIndex(i).Name
		nestedStructType := structType.Field(i).Type
		if nestedStructType.Kind() != reflect.Struct {
			return "", fmt.Errorf(
				"expected nested struct with `tablename:\"%s\"` to be a kind of Struct, but got %v",
				nestedStructName, nestedStructType,
			)
		}

		nestedStructInfo := kstructs.GetTagInfo(nestedStructType)
		for j := 0; j < structType.Field(i).Type.NumField(); j++ {
			fields = append(
				fields,
				dialect.Escape(nestedStructName)+"."+dialect.Escape(nestedStructInfo.ByIndex(j).Name),
			)
		}
	}

	return "SELECT " + strings.Join(fields, ", ") + " ", nil
}
