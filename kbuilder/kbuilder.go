package kbuilder

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/vingarcia/ksql"
	"github.com/vingarcia/ksql/kstructs"
)

// Builder is the basic container for injecting
// query builder configurations.
//
// All the Query structs can also be called
// directly without this builder, but we kept it
// here for convenience.
type Builder struct {
	dialect ksql.Dialect
}

// New creates a new Builder container.
func New(driver string) (Builder, error) {
	dialect, err := ksql.GetDriverDialect(driver)
	return Builder{
		dialect: dialect,
	}, err
}

// Build receives a query builder struct, injects it with the configurations
// build the query according to its arguments.
func (builder *Builder) Build(query Query) (sqlQuery string, params []interface{}, _ error) {
	var b strings.Builder

	switch v := query.Select.(type) {
	case string:
		b.WriteString("SELECT " + v)
	default:
		selectQuery, err := buildSelectQuery(v, builder.dialect)
		if err != nil {
			return "", nil, errors.Wrap(err, "error reading the Select field")
		}
		b.WriteString("SELECT " + selectQuery)
	}

	b.WriteString(" FROM " + query.From)

	if len(query.Where) > 0 {
		var whereQuery string
		whereQuery, params = query.Where.build(builder.dialect)
		b.WriteString(" WHERE " + whereQuery)
	}

	if strings.TrimSpace(query.From) == "" {
		return "", nil, fmt.Errorf("the From field is mandatory for every query")
	}

	if query.OrderBy.fields != "" {
		b.WriteString(" ORDER BY " + query.OrderBy.fields)
		if query.OrderBy.desc {
			b.WriteString(" DESC")
		}
	}

	if query.Limit > 0 {
		b.WriteString(" LIMIT " + strconv.Itoa(query.Limit))
	}

	if query.Offset > 0 {
		b.WriteString(" OFFSET " + strconv.Itoa(query.Offset))
	}

	return b.String(), params, nil
}

// Query is is the struct template for building SELECT queries.
type Query struct {
	// Select expects either a struct using the `ksql` tags
	// or a string listing the column names using SQL syntax,
	// e.g.: `id, username, address`
	Select interface{}

	// From expects the FROM clause from an SQL query, e.g. `users JOIN posts USING(post_id)`
	From string

	// Where expects a list of WhereQuery instances built
	// by the public Where() function.
	Where WhereQueries

	Limit   int
	Offset  int
	OrderBy OrderByQuery
}

// WhereQuery represents a single condition in a WHERE expression.
type WhereQuery struct {
	// Accepts any SQL boolean expression
	// This expression may optionally contain
	// string formatting directives %s and only %s.
	//
	// For each of these directives we expect a new param
	// on the params list below.
	//
	// In the resulting query each %s will be properly replaced
	// by placeholders according to the database driver, e.g. `$1`
	// for postgres or `?` for sqlite3.
	cond   string
	params []interface{}
}

// WhereQueries is the helper for creating complex WHERE queries
// in a dynamic way.
type WhereQueries []WhereQuery

func (w WhereQueries) build(dialect ksql.Dialect) (query string, params []interface{}) {
	var conds []string
	for _, whereQuery := range w {
		var placeholders []interface{}
		for i := range whereQuery.params {
			placeholders = append(placeholders, dialect.Placeholder(len(params)+i))
		}

		conds = append(conds, fmt.Sprintf(whereQuery.cond, placeholders...))
		params = append(params, whereQuery.params...)
	}

	return strings.Join(conds, " AND "), params
}

// Where adds a new bollean condition to an existing
// WhereQueries helper.
func (w WhereQueries) Where(cond string, params ...interface{}) WhereQueries {
	return append(w, WhereQuery{
		cond:   cond,
		params: params,
	})
}

// WhereIf condionally adds a new boolean expression to the WhereQueries helper.
func (w WhereQueries) WhereIf(cond string, param interface{}) WhereQueries {
	if param == nil || reflect.ValueOf(param).IsNil() {
		return w
	}

	return append(w, WhereQuery{
		cond:   cond,
		params: []interface{}{param},
	})
}

// Where adds a new bollean condition to an existing
// WhereQueries helper.
func Where(cond string, params ...interface{}) WhereQueries {
	return WhereQueries{{
		cond:   cond,
		params: params,
	}}
}

// WhereIf condionally adds a new boolean expression to the WhereQueries helper
func WhereIf(cond string, param interface{}) WhereQueries {
	if param == nil || reflect.ValueOf(param).IsNil() {
		return WhereQueries{}
	}

	return WhereQueries{{
		cond:   cond,
		params: []interface{}{param},
	}}
}

// OrderByQuery represents the ORDER BY part of the query
type OrderByQuery struct {
	fields string
	desc   bool
}

// Desc is a setter function for configuring the
// ORDER BY part of the query as DESC
func (o OrderByQuery) Desc() OrderByQuery {
	return OrderByQuery{
		fields: o.fields,
		desc:   true,
	}
}

// OrderBy is a helper for building the ORDER BY
// part of the query.
func OrderBy(fields string) OrderByQuery {
	return OrderByQuery{
		fields: fields,
		desc:   false,
	}
}

var cachedSelectQueries = map[reflect.Type]string{}

// Builds the select query using cached info so that its efficient
func buildSelectQuery(obj interface{}, dialect ksql.Dialect) (string, error) {
	t := reflect.TypeOf(obj)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		return "", fmt.Errorf("expected to receive a pointer to struct, but got: %T", obj)
	}

	if query, found := cachedSelectQueries[t]; found {
		return query, nil
	}

	info := kstructs.GetTagInfo(t)

	var escapedNames []string
	for i := 0; i < info.NumFields(); i++ {
		name := info.ByIndex(i).Name
		escapedNames = append(escapedNames, dialect.Escape(name))
	}

	query := strings.Join(escapedNames, ", ")
	cachedSelectQueries[t] = query
	return query, nil
}
