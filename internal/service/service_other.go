//go:build !linux && !darwin

package service

func platformManager(string) Manager { return nil }
