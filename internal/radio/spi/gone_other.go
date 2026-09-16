//go:build !linux

package spi

func deviceGone(error) bool { return false }
