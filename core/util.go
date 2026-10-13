package main

import "os"

func osReadFile(p string) ([]byte, error) { return os.ReadFile(p) }

func checkRetries() int   { return curRetries }
func checkServer() string { return curServer }
