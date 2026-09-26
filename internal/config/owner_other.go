//go:build !unix

package config

import "os"

func checkOwner(string, os.FileInfo) error { return nil }
