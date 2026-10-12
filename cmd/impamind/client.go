//go:build !client

package main

import "errors"

func clientMain([]string) error { return errors.New("client commands are built with -tags client") }
