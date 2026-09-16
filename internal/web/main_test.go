package web

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Tests sign in over and over: a cheap password hash keeps them fast (under -race especially).
	pbkdf2Iterations = 1000
	os.Exit(m.Run())
}
