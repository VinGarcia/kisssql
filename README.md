
# KissSQL

If the thing you hate the most when coding is having too much unnecessary
abstractions and the second thing you hate the most is having verbose
and repetitive code for routine tasks this library is probably for you.

Welcome to the KissSQL project, the "Keep It Stupid Simple" SQL client for Go.

This package was created to be used by any developer efficiently and safely.
The goals were:

- To be easy to use
- To be hard to make mistakes
- To have a small API so it's easy to learn
- To be easy to mock and test (very easy)
- And above all to be readable.

**Supported Drivers:**

Currently we support only the 4 most popular Golang database drivers:

- `"postgres"`
- `"sqlite3"`
- `"mysql"`
- `"sqlserver"`

If you need a new one included please open an issue or make
your own implementation and submit a Pull Request.

### Why KissSQL?

> Note: If you want numbers see our Benchmark section below

KissSQL was created to fill a hole between the complexity
we find in the tools I've seen so far, namely:

- ORMs such as `GORM` that do a lot and have literally hundreds
  of functions that require learning, increasing the risk of
  interpretation errors, learning time, complicating mocking, etc.
- Tools such as `sqlx` that do little but still have most of
  the quirks from the standard `sql` lib, requiring several
  error checks for each query which is very low level.

Besides both these examples were not created having
easy tests as one of the requisites, which might cause
your team to loose far more time than necessary writing
the tests or worst: Opting to not writing tests since
it would take too much time.

So the goal was to be high level enough that it would
avoid the complications from the `sql` package and
at the same time to be simple enough to avoid
the big learning curve and complexity of the hundreds
of functions offered by ORMs.

That said, KissSQL attempts to apply the Kiss principle,
in order to save development time for your team, i.e.:

- Less time spent learning (few methods to learn)
- Less time spent testing (helper tools made to help you)
- Less time spent debugging (simple apis are easier to debug)
- and less time reading & understanding the code

### Kiss Interface

The current interface is as follows and we plan on keeping
it with as little functions as possible, so don't expect many additions:

```go
// Provider describes the ksql public behavior
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
```

### Usage examples

This example is also available [here](./examples/crud/crud.go)
if you want to compile it yourself.

Also we have a small feature for building the "SELECT" part of the query if
you rather not use `SELECT *` queries, you may skip to the
[Select Generator Feature](#Select-Generator-Feature) which is very clean too.

```Go
package main

import (
	"context"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
	"github.com/vingarcia/ksql"
	"github.com/vingarcia/ksql/nullable"
)

// User ...
type User struct {
	ID   int    `ksql:"id"`
	Name string `ksql:"name"`
	Age  int    `ksql:"age"`

	// This field will be saved as JSON in the database
	Address Address `ksql:"address,json"`
}

// PartialUpdateUser ...
type PartialUpdateUser struct {
	ID      int      `ksql:"id"`
	Name    *string  `ksql:"name"`
	Age     *int     `ksql:"age"`
	Address *Address `ksql:"address,json"`
}

// Address ...
type Address struct {
	State string `json:"state"`
	City  string `json:"city"`
}

// UsersTable informs ksql the name of the table and that it can
// use the default value for the primary key column name: "id"
var UsersTable = ksql.NewTable("users")

func main() {
	ctx := context.Background()
	db, err := ksql.New("sqlite3", "/tmp/hello.sqlite", ksql.Config{
		MaxOpenConns: 1,

		// UseGolangPlaceholders allows you to use the same placeholder `%s`
		// for all databases which is useful if you want your code to work in
		// different platforms.
		//
		// Ignore or set this argument to false if you prefer
		// using the database specific placeholders like `$1`, `?` or `@p1`
		UseGolangPlaceholders: true,
	})
	if err != nil {
		panic(err.Error())
	}

	// In the definition below, please note that BLOB is
	// the only type we can use in sqlite for storing JSON.
	err = db.Exec(ctx, `CREATE TABLE IF NOT EXISTS users (
	  id INTEGER PRIMARY KEY,
		age INTEGER,
		name TEXT,
		address BLOB
	)`)
	if err != nil {
		panic(err.Error())
	}

	var alison = User{
		Name: "Alison",
		Age:  22,
		Address: Address{
			State: "MG",
		},
	}
	err = db.Insert(ctx, UsersTable, &alison)
	if err != nil {
		panic(err.Error())
	}
	fmt.Println("Alison ID:", alison.ID)

	// Inserting inline:
	err = db.Insert(ctx, UsersTable, &User{
		Name: "Cristina",
		Age:  27,
		Address: Address{
			State: "SP",
		},
	})
	if err != nil {
		panic(err.Error())
	}

	// Deleting Alison:
	err = db.Delete(ctx, UsersTable, alison.ID)
	if err != nil {
		panic(err.Error())
	}

	// Retrieving Cristina:
	var cris User
	err = db.QueryOne(ctx, &cris, "SELECT * FROM users WHERE name = %s ORDER BY id", "Cristina")
	if err != nil {
		panic(err.Error())
	}
	fmt.Printf("Cristina: %#v\n", cris)

	// Updating all fields from Cristina:
	cris.Name = "Cris"
	err = db.Update(ctx, UsersTable, cris)

	// Changing the age of Cristina but not touching any other fields:

	// Partial update technique 1:
	err = db.Update(ctx, UsersTable, struct {
		ID  int `ksql:"id"`
		Age int `ksql:"age"`
	}{ID: cris.ID, Age: 28})
	if err != nil {
		panic(err.Error())
	}

	// Partial update technique 2:
	err = db.Update(ctx, UsersTable, PartialUpdateUser{
		ID:  cris.ID,
		Age: nullable.Int(28),
	})
	if err != nil {
		panic(err.Error())
	}

	// Listing first 10 users from the database
	// (each time you run this example a new Cristina is created)
	//
	// Note: Using this function it is recommended to set a LIMIT, since
	// not doing so can load too many users on your computer's memory or
	// cause an Out Of Memory Kill.
	//
	// If you need to query very big numbers of users we recommend using
	// the `QueryChunks` function.
	var users []User
	err = db.Query(ctx, &users, "SELECT * FROM users LIMIT 10")
	if err != nil {
		panic(err.Error())
	}

	// Making transactions:
	err = db.Transaction(ctx, func(db ksql.Provider) error {
		var cris2 User
		err = db.QueryOne(ctx, &cris2, "SELECT * FROM users WHERE id = %s", cris.ID)
		if err != nil {
			// This will cause an automatic rollback:
			return err
		}

		err = db.Update(ctx, UsersTable, PartialUpdateUser{
			ID:  cris2.ID,
			Age: nullable.Int(29),
		})
		if err != nil {
			// This will also cause an automatic rollback and then panic again
			// so that we don't hide the panic inside the KissSQL library
			panic(err.Error())
		}

		// Commits the transaction
		return nil
	})
	if err != nil {
		panic(err.Error())
	}

	fmt.Printf("Users: %#v\n", users)
}
```

### Query Chunks Feature

It's very unsual for us to need to load a number of records from the
database that might be too big for fitting in memory, e.g. load all the
users and send them somewhere. But it might happen.

For these cases it's best to load chunks of data at a time so
that we can work on a substantial amount of data at a time and never
overload our memory capacity. For this use case we have a specific
function called `QueryChunks`:

```golang
err = db.QueryChunks(ctx, ksql.ChunkParser{
	Query:     "SELECT * FROM users WHERE type = ?",
	Params:    []interface{}{usersType},
	ChunkSize: 100,
	ForEachChunk: func(users []User) error {
		err := sendUsersSomewhere(users)
		if err != nil {
			// This will abort the QueryChunks loop and return this error
			return err
		}
		return nil
	},
})
if err != nil {
	panic(err.Error())
}
```

It's signature is more complicated than the other two Query\* methods,
thus, it is adivisible to always prefer using the other two when possible
reserving this one for the rare use-case where you are actually
loading big sections of the database into memory.

### Select Generator Feature

There are good reasons not to use `SELECT *` queries the most important
of them is that you might end up loading more information than you are actually
going to use putting more pressure in your database for no good reason.

To prevent that `ksql` has a feature specifically for building the `SELECT`
part of the query using the tags from the input struct.
Using it is very simple and it works with all the 3 Query\* functions:

Querying a single user:

```golang
var user User
err = db.QueryOne(ctx, &user, "FROM users WHERE id = ?", userID)
if err != nil {
	panic(err.Error())
}
```

Querying a page of users:

```golang
var users []User
err = db.Query(ctx, &users, "FROM users WHERE type = ? ORDER BY id LIMIT ? OFFSET ?", "Cristina", limit, offset)
if err != nil {
	panic(err.Error())
}
```

Querying all the users, or any potentially big number of users, from the database (not usual, but supported):

```golang
err = db.QueryChunks(ctx, ksql.ChunkParser{
	Query:     "FROM users WHERE type = ?",
	Params:    []interface{}{usersType},
	ChunkSize: 100,
	ForEachChunk: func(users []User) error {
		err := sendUsersSomewhere(users)
		if err != nil {
			// This will abort the QueryChunks loop and return this error
			return err
		}
		return nil
	},
})
if err != nil {
	panic(err.Error())
}
```

The implementation of this feature is actually simple internally.
First we check if the query is starting with the word `FROM`,
if it is then we just get the `ksql` tags from the struct and
then use it for building the `SELECT` statement.

The `SELECT` statement is then cached so we don't have to build it again
the next time in order to keep the library efficient even when
using this feature.

### Select Generation with Joins

So there is one use-case that was not covered by `ksql` so far:

What if you want to JOIN multiple tables for which you already have
structs defined? Would you need to create a new struct to represent
the joined columns of the two tables? no, we actually have this covered as well.

`ksql` has a special feature for allowing the reuse of existing
structs by using composition in an anonymous struct, and then
generating the `SELECT` part of the query accordingly:

Querying a single joined row:

```golang
var row struct{
	User User `tablename:"u"`     // (here the tablename must match the aliased tablename in the query)
	Post Post `tablename:"posts"` // (if no alias is used you should use the actual name of the table)
}
err = db.QueryOne(ctx, &row, "FROM users as u JOIN posts ON u.id = posts.user_id WHERE u.id = ?", userID)
if err != nil {
	panic(err.Error())
}
```

Querying a page of joined rows:

```golang
var rows []struct{
	User User `tablename:"u"`
	Post Post `tablename:"p"`
}
err = db.Query(ctx, &rows,
	"FROM users as u JOIN posts as p ON u.id = p.user_id WHERE name = ? LIMIT ? OFFSET ?",
	"Cristina", limit, offset,
)
if err != nil {
	panic(err.Error())
}
```

Querying all the users, or any potentially big number of users, from the database (not usual, but supported):

```golang
err = db.QueryChunks(ctx, ksql.ChunkParser{
	Query:     "FROM users as u JOIN posts as p ON u.id = p.user_id WHERE type = ?",
	Params:    []interface{}{usersType},
	ChunkSize: 100,
	ForEachChunk: func(rows []struct{
		User User `tablename:"u"`
		Post Post `tablename:"p"`
	}) error {
		err := sendRowsSomewhere(rows)
		if err != nil {
			// This will abort the QueryChunks loop and return this error
			return err
		}
		return nil
	},
})
if err != nil {
	panic(err.Error())
}
```

As advanced as this feature might seem we don't do any parsing of the query,
and all the work is done only once and then cached.

What actually happens is that we use the "tablename" tag to build the `SELECT`
part of the query like this:

- `SELECT u.id, u.name, u.age, p.id, p.title `

This is then cached, and when we need it again we concatenate it with the rest
of the query.

This feature has two important limitations:

1. It is not possible to use `tablename` tags together with normal `ksql` tags.
   Doing so will cause the `tablename` tags to be ignored in favor of the `ksql` ones.
2. It is not possible to use it without omitting the `SELECT` part of the query.
   While in normal queries we match the selected field with the attribute by name,
   in queries joining multiple tables we can't use this strategy because
   different tables might have columns with the same name, and we don't
   really have access to the full name of these columns making, for example,
   it impossible to differentiate between `u.id` and `p.id` except by the
   order in which these fields were passed. Thus, it is necessary that
   the library itself writes the `SELECT` part of the query when using
   this technique so that we can control the order or the selected fields.

Ok, but what if I don't want to use this feature?

You are not forced to, and there are a few use-cases where you would prefer not to, e.g.:

```golang
var rows []struct{
	UserName string `ksql:"name"`
	PostTitle string `ksql:"title"`
}
err := db.Query(ctx, &rows, "SELECT u.name, p.title FROM users u JOIN posts p ON u.id = p.user_id LIMIT 10")
if err != nil {
	panic(err.Error())
}
```

In the example above, since we are only interested in a couple of columns it
is far simpler and more efficient for the database to only select the columns
that we actually care about, so it's better not to use composite kstructs.

### Testing Examples

This library has a few helper functions for helping your tests:

- `kstructs.FillStructWith(struct interface{}, dbRow map[string]interface{}) error`
- `kstructs.FillSliceWith(structSlice interface{}, dbRows []map[string]interface{}) error`
- `kstructs.StructToMap(struct interface{}) (map[string]interface{}, error)`
- `kstructs.CallFunctionWithRows(fn interface{}, rows []map[string]interface{}) (map[string]interface{}, error)`

If you want to see examples (we have examples for all the public functions) just
read the example tests available on our [example service](./examples/example_service)

### Benchmark Comparison

The benchmark is very good, the code is, in practical terms, as fast as sqlx:

```bash
$ make bench TIME=5s
go test -bench=. -benchtime=5s
goos: linux
goarch: amd64
pkg: github.com/vingarcia/ksql
cpu: Intel(R) Core(TM) i5-3210M CPU @ 2.50GHz
BenchmarkInsert/ksql-setup/insert-one-4         	    5293	    960859 ns/op
BenchmarkInsert/pgx-adapter-setup/insert-one-4  	    7982	    736973 ns/op
BenchmarkInsert/sqlx-setup/insert-one-4         	    6854	    857824 ns/op
BenchmarkQuery/ksql-setup/single-row-4          	   12596	    407116 ns/op
BenchmarkQuery/ksql-setup/multiple-rows-4       	   15883	    391135 ns/op
BenchmarkQuery/pgx-adapter-setup/single-row-4   	   34008	    165604 ns/op
BenchmarkQuery/pgx-adapter-setup/multiple-rows-4	   22579	    280673 ns/op
BenchmarkQuery/sqlx-setup/single-row-4          	   10000	    512741 ns/op
BenchmarkQuery/sqlx-setup/multiple-rows-4       	   10779	    596377 ns/op
PASS
ok  	github.com/vingarcia/ksql	94.951s
Benchmark executed at: 2021-08-01
Benchmark executed on commit: 37298e2c243f1ec66e88dd92ed7c4542f7820b4f
```

### Running the ksql tests (for contributors)

The tests run in dockerized database instances so the easiest way
to have them working is to just start them using docker-compose:

```bash
docker-compose up -d
```

And then for each of them you will need to run the command:

```sql
CREATE DATABASE ksql;
```

After that you can just run the tests by using:

```bash
make test
```

### TODO List

- Add tests for tables using composite keys
- Add support for serializing structs as other formats such as YAML
- Update `kstructs.FillStructWith` to work with `ksql:"..,json"` tagged attributes
- Make testing easier by exposing the connection strings in an .env file
- Make testing easier by automatically creating the `ksql` database
- Create a way for users to submit user defined dialects
- Improve error messages
- Add support for the update function to work with maps for partial updates
- Add support for the insert function to work with maps
- Add support for a `ksql.Array(params ...interface{})` for allowing queries like this:
  `db.Query(ctx, &user, "SELECT * FROM user WHERE id in (?)", ksql.Array(1,2,3))`

### Optimization Oportunities

- Test if using a pointer on the field info is faster or not
- Consider passing the cached structInfo as argument for all the functions that use it,
  so that we don't need to get it more than once in the same call.
- Use a cache to store all queries after they are built
- Preload the insert method for all dialects inside `ksql.NewTable()`
