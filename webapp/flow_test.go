package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestMysqlErrorIs(t *testing.T) {
	dup := &mysql.MySQLError{Number: mysqlErrDuplicate, Message: "Duplicate entry"}
	for _, c := range []struct {
		err    error
		number uint16
		want   bool
	}{
		{dup, mysqlErrDuplicate, true},
		{fmt.Errorf("save: %w", dup), mysqlErrDuplicate, true},
		{dup, mysqlErrForeignKey, false},
		{errors.New("Error 1062: Duplicate entry"), mysqlErrDuplicate, false},
		{nil, mysqlErrDuplicate, false},
	} {
		if got := mysqlErrorIs(c.err, c.number); got != c.want {
			t.Errorf("mysqlErrorIs(%v, %d) = %v, want %v", c.err, c.number, got, c.want)
		}
	}
}
