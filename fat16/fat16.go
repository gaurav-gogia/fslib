// Copyright (c) 2019-2020 Siemens AG
// Copyright (c) 2019-2021 Jonas Plum
//
// Permission is hereby granted, free of charge, to any person obtaining a copy of
// this software and associated documentation files (the "Software"), to deal in
// the Software without restriction, including without limitation the rights to
// use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
// the Software, and to permit persons to whom the Software is furnished to do so,
// subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
// FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
// COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
// IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
// CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
//
// Author(s): Jonas Plum

package fat16

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type volumeHeader struct {
	JumpInstruction       [3]byte
	CreatingSystemID      [8]byte
	SectorSize            uint16
	SectorsPerCluster     byte
	ReservedSectorCount   uint16
	FatCount              int8
	RootdirEntryCount     uint16
	SectorCountSmall      uint16
	MediaID               byte
	SectorsPerFat         uint16
	SectorsPerTrack       uint16
	SideCount             uint16
	HiddenSectorCount     uint32
	SectorCountLarge      uint32
	PhysicalDriveNumber   byte
	CurrentHead           byte
	ExtendedBootSignature byte
	VolumeID              [4]byte
	VolumeLabel           [11]byte
	FsType                [8]byte
	BootCode              [448]byte
	BootSectorSignature   [2]byte
}

type lfnEntry struct {
	SequenceNumber  uint8
	Filename1       [10]byte
	Attributes      byte
	Type            byte
	Checksum        byte
	Filename2       [12]byte
	Startingcluster uint16
	Filename3       [4]byte
}

// func (m *FS) getDirectoryEntry(cluster int64, count uint16, name string) (filename string, de *namedEntry, err error) {
func (m *FS) getDirectoryEntry(offset int64, count uint16, name string) (filename string, de *namedEntry, err error) {
	if count == 0 {
		count = m.vh.SectorSize * uint16(m.vh.SectorsPerCluster)
	}
	entries, err := m.getDirectoryEntries(offset, count)

	pathParts := strings.SplitN(name, "/", 2)
	currentName := pathParts[0]
	if de, ok := entries[currentName]; ok {
		if len(pathParts) > 1 {
			offset := getOffset(int64(de.Startingcluster), m.vh)
			return m.getDirectoryEntry(offset, count, pathParts[1])
		}
		return currentName, de, err
	}

	return currentName, nil, errors.New("file not found")
}

func (m *FS) getDirectoryEntries(offset int64, count uint16) (map[string]*namedEntry, error) {
	_, err := m.decoder.Seek(offset, os.SEEK_SET)
	if err != nil {
		return nil, err
	}

	files := map[string]*namedEntry{}

	var currentFilename []byte
	for i := uint16(0); i < count; i++ {
		firstByte, err := firstByte(m.decoder)
		if err != nil {
			return nil, err
		}

		// if firstByte == 0xe5 { } TODO: Handle deleted files

		// test if entry exists
		if firstByte != 0x00 {
			de := directoryEntry{}

			data := make([]byte, 32)
			_, err = m.decoder.Read(data)
			if err != nil {
				return nil, err
			}
			m.decoder.Seek(-32, os.SEEK_CUR) // nolint: errcheck

			err := binary.Read(m.decoder, binary.LittleEndian, &de)
			if err != nil {
				return nil, err
			}

			// long filename
			if de.FileAttributes == 0x0F && de.Startingcluster == 0x00 {
				currentFilename, err = handleLongFilname(data, currentFilename)
				if err != nil {
					return nil, err
				}
				continue
			}

			// if de.FileAttributes&0x08 != 0 { } hide volume label

			// get filename
			filename := formatFilename(&de)
			if len(currentFilename) != 0 {
				filename = strings.TrimRight(utf16BytesToString(currentFilename, binary.LittleEndian), "\x00")
				currentFilename = []byte{}
			}

			// log.Print("filename ", filename, " ", de.FileAttributes&0x10 != 0, de.Startingcluster, getOffset(int64(de.Startingcluster), m.vh))
			files[filename] = &namedEntry{name: filename, directoryEntry: de}
		} else {
			_, err = m.decoder.Seek(32, os.SEEK_CUR)
			if err != nil {
				return nil, err
			}
		}
	}
	return files, nil
}

func firstByte(data io.ReadSeeker) (byte, error) {
	// get first byte
	firstByteA := make([]byte, 1)
	n, err := data.Read(firstByteA)
	if err != nil && err != io.EOF {
		return 0, err
	}
	if n == 0 {
		return 0, io.EOF
	}
	_, err = data.Seek(-1, 1)
	if err != nil {
		return 0, err
	}
	return firstByteA[0], nil
}

func utf16BytesToString(b []byte, o binary.ByteOrder) string {
	utf := make([]uint16, (len(b)+(2-1))/2)
	for i := 0; i+(2-1) < len(b); i += 2 {
		utf[i/2] = o.Uint16(b[i:])
	}
	if len(b)/2 < len(utf) {
		utf[len(utf)-1] = utf8.RuneError
	}
	return string(utf16.Decode(utf))
}

func handleLongFilname(data []byte, currentFilename []byte) ([]byte, error) {
	lfn := lfnEntry{}
	err := binary.Read(bytes.NewBuffer(data), binary.LittleEndian, &lfn)
	if err != nil && err != io.EOF {
		return nil, err
	}

	lname := append(bytes.TrimRight(lfn.Filename1[:], "\xff"), bytes.TrimRight(lfn.Filename2[:], "\xff")...)
	lname = append(lname, bytes.TrimRight(lfn.Filename3[:], "\xff")...)
	currentFilename = append(lname, currentFilename...)
	return currentFilename, nil
}

func fatOffset(vh volumeHeader) int64 {
	return int64(vh.ReservedSectorCount) * int64(vh.SectorSize)
}

func rootOffset(vh volumeHeader) int64 {
	return int64(vh.SectorsPerFat)*int64(vh.FatCount)*int64(vh.SectorSize) + fatOffset(vh)
}

func getOffset(cluster int64, vh volumeHeader) int64 {
	rootDirSectors := (vh.RootdirEntryCount*32 + (vh.SectorSize - 1)) / vh.SectorSize

	firstDataByte := rootOffset(vh) + int64(rootDirSectors)*int64(vh.SectorSize)

	return ((cluster-2)*int64(vh.SectorsPerCluster))*int64(vh.SectorSize) + firstDataByte
}

/*
func (m *FS) getVolumeName() (string, error) {
	rootDirStart := (int64(m.vh.SectorsPerFat)*int64(m.vh.FatCount) + 1) * 512

	_, err := decoder.Seek(rootDirStart, os.SEEK_SET)
	if err != nil {
		return "", err
	}

	for i := uint16(0); i < 5; i++ {
		firstByte, err := firstByte(decoder)
		if err != nil {
			return "", err
		}

		// test if entry exists
		if firstByte != 0x00 {
			de := directoryEntry{}

			err := binary.Read(decoder, binary.LittleEndian, &de)
			if err != nil {
				return "", err
			}

			// hide volume label
			if de.FileAttributes&0x08 != 0 {
				return formatFilename(&de), nil
			}
		}
	}
	return "", errors.New("Volumename not found")
}
*/

/*
func handleEntry(firstByte byte) {
	// parse directory entry
	de := directoryEntry{}
	err := binary.Read(m.decoder, binary.LittleEndian, &de)
	if err != nil && err != io.EOF {
		panic(err)
	}

	// get filename
	filename := formatFilename(&de)

	// skip parent folders
	if filename == "." || filename == ".." {
		return
	}

	// create child
	child := core.NewItem(item.URL+"/"+filename, &extractors.ByteReader{Data: []byte{}})

	// test if item is deleted
	if firstByte == 0xe5 {
		//process deleted items
		processDeletedDirectoryEntry(child)

	} else {
		// process normal items
		processDirectoryEntry(&de, child, item)
	}
}

func processDeletedDirectoryEntry(child *core.Item, itemStore itemstore.ItemStore) {
	child.Attr["Deleted"] = true
}

func processDirectoryEntry(de *directoryEntry, child *core.Item, item *core.Item, itemStore itemstore.ItemStore) {
	if de.FileAttributes == 0x0F && de.Startingcluster == 0x00 {
		/*
			olog.Logger.Infof("VFAT LFN")

			sequenceNumber := int8(rootDir[32*i] & 0x1F)
			olog.Logger.Infof("sequenceNumber %d", sequenceNumber)
		* /
	} else {
		// file or folder

		// Volume label
		if de.FileAttributes&0x08 != 0 {
			return
		}
		if de.FileAttributes&0x01 != 0 {
			child.Attr["fatdir.readOnly"] = true
		}
		if de.FileAttributes&0x02 != 0 {
			child.Attr["fatdir.hidden"] = true
		}
		if de.FileAttributes&0x04 != 0 {
			child.Attr["fatdir.system"] = true
		}

		if de.FileAttributes&0x08 != 0 {
			child.Attr["fatdir.volumeLabel"] = true
		}
		if de.FileAttributes&0x10 != 0 {
			child.Attr["fatdir.subdirectory"] = true
			fatDir := FATDIR{}
			child.Attr["item.type"] = fatDir.MIMEType()
		}

		// get FAT
		faturl := item.Attr["fat16.faturl"].(string)
		fat, err := itemStore.Load(faturl)
		if err != nil {
			item.Panic(err)
		}

		// calc fragments
		fileAllocationTable := ProcessFat(fat)
		clusters := getClusters(fileAllocationTable, uint64(de.Startingcluster))
		bytesPerCluster := uint64(item.Attr["fat16.sectorsPerCluster"].(byte)) * 512
		fragments := extractors.IntsToFragments(clusters)
		for i := range fragments {
			fragments[i].Start = item.Attr["fat16.clusterStart"].(uint64) + (fragments[i].Start-2)*bytesPerCluster
			fragments[i].Length *= bytesPerCluster
		}

		child.Data = &extractors.FragmentsReader{
			Parent:    item.Data.BaseExtractor(),
			Fragments: fragments,
		}
		child.Attr["fat16.clusterStart"] = item.Attr["fat16.clusterStart"]
		child.Attr["fat16.sectorsPerCluster"] = item.Attr["fat16.sectorsPerCluster"]
		child.Attr["fat16.faturl"] = item.Attr["fat16.faturl"]

		itemStore.Append(child)
		olog.Logger.Debugf("fatdir: child %s, fragments %v", child.URL, fragments)
	}

}

func getClusters(fattable []uint16, curBlock uint64) []uint64 {
	var clusters []uint64
	for 0 < curBlock && curBlock <= 0xfff8 {

		// add block to clusters of the file
		clusters = append(clusters, curBlock)
		curBlock = uint64(fattable[curBlock])
	}
	return clusters
}
*/
