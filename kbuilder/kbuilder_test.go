package kbuilder_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tj/assert"
	"github.com/vingarcia/ksql/kbuilder"
)

type User struct {
	Name string `ksql:"name"`
	Age  string `ksql:"age"`
}

var nullField *int

func TestBuilder(t *testing.T) {

	tests := []struct {
		desc           string
		query          kbuilder.Query
		expectedQuery  string
		expectedParams []interface{}
		expectedErr    bool
	}{
		{
			desc: "should build queries correctly",
			query: kbuilder.Query{
				Select: &User{},
				From:   "users",
				Where: kbuilder.
					Where("foo < %s", 42).
					Where("bar LIKE %s", "%ending").
					WhereIf("foobar = %s", nullField),

				OrderBy: kbuilder.OrderBy("id").Desc(),
				Offset:  100,
				Limit:   10,
			},
			expectedQuery:  `SELECT "name", "age" FROM users WHERE foo < $1 AND bar LIKE $2 ORDER BY id DESC LIMIT 10 OFFSET 100`,
			expectedParams: []interface{}{42, "%ending"},
		},
		{
			desc: "should build queries omitting the OFFSET",
			query: kbuilder.Query{
				Select: &User{},
				From:   "users",
				Where: kbuilder.
					Where("foo < %s", 42).
					Where("bar LIKE %s", "%ending").
					WhereIf("foobar = %s", nullField),

				OrderBy: kbuilder.OrderBy("id").Desc(),
				Limit:   10,
			},
			expectedQuery:  `SELECT "name", "age" FROM users WHERE foo < $1 AND bar LIKE $2 ORDER BY id DESC LIMIT 10`,
			expectedParams: []interface{}{42, "%ending"},
		},
		{
			desc: "should build queries omitting the LIMIT",
			query: kbuilder.Query{
				Select: &User{},
				From:   "users",
				Where: kbuilder.
					Where("foo < %s", 42).
					Where("bar LIKE %s", "%ending").
					WhereIf("foobar = %s", nullField),

				OrderBy: kbuilder.OrderBy("id").Desc(),
				Offset:  100,
			},
			expectedQuery:  `SELECT "name", "age" FROM users WHERE foo < $1 AND bar LIKE $2 ORDER BY id DESC OFFSET 100`,
			expectedParams: []interface{}{42, "%ending"},
		},
		{
			desc: "should build queries omitting the ORDER BY clause",
			query: kbuilder.Query{
				Select: &User{},
				From:   "users",
				Where: kbuilder.
					Where("foo < %s", 42).
					Where("bar LIKE %s", "%ending").
					WhereIf("foobar = %s", nullField),

				Offset: 100,
				Limit:  10,
			},
			expectedQuery:  `SELECT "name", "age" FROM users WHERE foo < $1 AND bar LIKE $2 LIMIT 10 OFFSET 100`,
			expectedParams: []interface{}{42, "%ending"},
		},
		{
			desc: "should build queries omitting the WHERE clause",
			query: kbuilder.Query{
				Select: &User{},
				From:   "users",

				OrderBy: kbuilder.OrderBy("id").Desc(),
				Offset:  100,
				Limit:   10,
			},
			expectedQuery: `SELECT "name", "age" FROM users ORDER BY id DESC LIMIT 10 OFFSET 100`,
		},

		/* * * * * Testing error cases: * * * * */
		{
			desc: "should report error if the FROM clause is missing",
			query: kbuilder.Query{
				Select: &User{},
				Where: kbuilder.
					Where("foo < %s", 42).
					Where("bar LIKE %s", "%ending").
					WhereIf("foobar = %s", nullField),

				OrderBy: kbuilder.OrderBy("id").Desc(),
				Offset:  100,
				Limit:   10,
			},

			expectedErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			b, err := kbuilder.New("postgres")
			assert.Equal(t, nil, err)

			query, params, err := b.Build(test.query)

			expectError(t, test.expectedErr, err)
			assert.Equal(t, test.expectedQuery, query)
			assert.Equal(t, test.expectedParams, params)
		})
	}
}

func expectError(t *testing.T, expect bool, err error) {
	if expect {
		require.Equal(t, true, err != nil, "expected an error, but got nothing")
	} else {
		require.Equal(t, false, err != nil, fmt.Sprintf("unexpected error %s", err))
	}
}
