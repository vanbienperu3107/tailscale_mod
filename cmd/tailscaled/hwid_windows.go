// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows control codes / property enums for querying a physical disk.
const (
	ioctlStorageGetDeviceNumber = 0x2D1080 // IOCTL_STORAGE_GET_DEVICE_NUMBER
	ioctlStorageQueryProperty   = 0x2D1400 // IOCTL_STORAGE_QUERY_PROPERTY
	storageDeviceProperty       = 0        // StorageDeviceProperty
	propertyStandardQuery       = 0        // PropertyStandardQuery
)

// storageDeviceNumber mirrors the Win32 STORAGE_DEVICE_NUMBER: the physical
// drive number backing an opened volume handle.
type storageDeviceNumber struct {
	DeviceType      uint32
	DeviceNumber    uint32
	PartitionNumber int32
}

// storagePropertyQuery is the input for IOCTL_STORAGE_QUERY_PROPERTY. It mirrors
// the Win32 STORAGE_PROPERTY_QUERY exactly, including the trailing
// AdditionalParameters[1] BYTE: Windows validates InputBufferLength against the
// full sizeof(STORAGE_PROPERTY_QUERY) (12 bytes with padding), so sending a
// truncated 8-byte struct can be rejected as an invalid parameter.
type storagePropertyQuery struct {
	PropertyID           uint32
	QueryType            uint32
	AdditionalParameters [1]byte
}

// storageDeviceDescriptor is the fixed header of the STORAGE_DEVICE_DESCRIPTOR
// returned by IOCTL_STORAGE_QUERY_PROPERTY. SerialNumberOffset is a byte offset
// (from the start of the returned buffer) to a NUL-terminated ASCII serial, or
// 0 when the device reports none.
type storageDeviceDescriptor struct {
	Version               uint32
	Size                  uint32
	DeviceType            byte
	DeviceTypeModifier    byte
	RemovableMedia        byte
	CommandQueueing       byte
	VendorIDOffset        uint32
	ProductIDOffset       uint32
	ProductRevisionOffset uint32
	SerialNumberOffset    uint32
	BusType               uint32
	RawPropertiesLength   uint32
}

// machineHardwareID returns the serial number of the physical disk that backs
// the OS volume (%SystemDrive%, normally C:). This is the stable per-machine
// anchor for the deterministic machine key (see hwid.go). Returns ("", nil)
// when the OS reports no serial (some VMs / controllers) — the caller then
// falls back to a random key rather than treating "no serial" as fatal.
func machineHardwareID() (string, error) {
	sysDrive := os.Getenv("SystemDrive") // e.g. "C:"
	if sysDrive == "" {
		sysDrive = "C:"
	}
	driveNum, err := osVolumePhysicalDriveNumber(sysDrive)
	if err != nil {
		return "", err
	}
	return physicalDriveSerial(driveNum)
}

// osVolumePhysicalDriveNumber resolves a drive letter (e.g. "C:") to the number
// of the physical disk that holds it, via IOCTL_STORAGE_GET_DEVICE_NUMBER.
func osVolumePhysicalDriveNumber(driveLetter string) (uint32, error) {
	// \\.\C:  — the volume device. No access rights are needed for this IOCTL,
	// so open with 0 access (works without administrator).
	path := `\\.\` + strings.TrimRight(driveLetter, `\`)
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(p, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer windows.CloseHandle(h)

	var sdn storageDeviceNumber
	var ret uint32
	if err := windows.DeviceIoControl(h, ioctlStorageGetDeviceNumber,
		nil, 0,
		(*byte)(unsafe.Pointer(&sdn)), uint32(unsafe.Sizeof(sdn)),
		&ret, nil); err != nil {
		return 0, fmt.Errorf("IOCTL_STORAGE_GET_DEVICE_NUMBER on %s: %w", path, err)
	}
	return sdn.DeviceNumber, nil
}

// physicalDriveSerial reads the serial number of \\.\PhysicalDrive<n> via
// IOCTL_STORAGE_QUERY_PROPERTY. Returns ("", nil) if the drive reports none.
func physicalDriveSerial(driveNum uint32) (string, error) {
	path := fmt.Sprintf(`\\.\PhysicalDrive%d`, driveNum)
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(p, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer windows.CloseHandle(h)

	query := storagePropertyQuery{
		PropertyID: storageDeviceProperty,
		QueryType:  propertyStandardQuery,
	}
	buf := make([]byte, 1024)
	var ret uint32
	if err := windows.DeviceIoControl(h, ioctlStorageQueryProperty,
		(*byte)(unsafe.Pointer(&query)), uint32(unsafe.Sizeof(query)),
		&buf[0], uint32(len(buf)),
		&ret, nil); err != nil {
		return "", fmt.Errorf("IOCTL_STORAGE_QUERY_PROPERTY on %s: %w", path, err)
	}
	if ret < uint32(unsafe.Sizeof(storageDeviceDescriptor{})) {
		return "", fmt.Errorf("%s: short descriptor (%d bytes)", path, ret)
	}
	desc := (*storageDeviceDescriptor)(unsafe.Pointer(&buf[0]))
	off := desc.SerialNumberOffset
	if off == 0 || off >= ret {
		return "", nil // device reports no serial
	}
	raw := buf[off:ret]
	if i := bytes.IndexByte(raw, 0); i >= 0 {
		raw = raw[:i]
	}
	return string(raw), nil
}
