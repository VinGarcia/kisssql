package ksql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pkg/errors"
)

// ErrRecordNotFound ...
var ErrRecordNotFound error = errors.Wrap(sql.ErrNoRows, "ksql: the query returned no results")

// ErrAbortIteration ...
var ErrAbortIteration error = fmt.Errorf("ksql: abort iteration, should only be used inside QueryChunks function")

// Provider describes the public behavior of this ORM
type Provider interface {
	Insert(ctx context.Context, table Table, record interface{}) error
	Update(ctx context.Context, table Table, record interface{}) error
	Delete(ctx context.Context, table Table, idsOrRecords ...interface{}) error

	Query(ctx context.Context, records interface{}, query string, params ...interface{}) error
	QueryOne(ctx context.Context, record interface{}, query string, params ...interface{}) error
	QueryChunks(ctx context.Context, parser ChunkParser) error

	Exec(ctx context.Context, query string, params ...interface{}) error
	Transaction(ctx context.Context, fn func(Provider) error) error
}

// Table describes the required information for inserting, updating and
// deleting entities from the database by ID using the 3 helper functions
// created for that purpose.
type Table struct {
	// this name must be set in order to use the Insert, Delete and Update helper
	// functions. If you only intend to make queries or to use the Exec function
	// it is safe to leave this field unset.
	name string

	// IDColumns defaults to []string{"id"} if unset
	idColumns []string
}

// NewTable returns a Table instance that stores
// the tablename and the names of columns used as ID,
// if no column name is passed it defaults to using
// the `"id"` column.
//
// This Table is required only for using the helper methods:
//
// - Insert
// - Update
// - Delete
//
// Passing multiple ID columns will be interpreted
// as a single composite key, if you want
// to use the helper functions with different
// keys you'll need to create multiple Table instances
// for the same database table, each with a different
// set of ID columns, but this is usually not necessary.
func NewTable(tableName string, ids ...string) Table {
	if len(ids) == 0 {
		ids = []string{"id"}
	}

	return Table{
		name:      tableName,
		idColumns: ids,
	}
}

func (t Table) insertMethodFor(dialect Dialect) insertMethod {
	if len(t.idColumns) == 1 {
		return dialect.InsertMethod()
	}

	insertMethod := dialect.InsertMethod()
	if insertMethod == insertWithLastInsertID {
		return insertWithNoIDRetrieval
	}

	return insertMethod
}

// ChunkParser stores the arguments of the QueryChunks function
type ChunkParser struct {
	// The Query and Params are used together to build a query with
	// protection from injection, just like when using the Find function.
	Query  string
	Params []interface{}

	ChunkSize int

	// This attribute must be a function with the following signature:
	//
	// `func(chunk []<Record>) error`.
	//
	// Where the actual Record type should be of a struct
	// representing the rows you are expecting to receive.
	ForEachChunk interface{}
}
