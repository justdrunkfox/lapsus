// Package dict provides word frequency dictionaries for EN and RU,
// used to detect words typed in the wrong keyboard layout.
package dict

import (
	"bufio"
	"embed"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/voev/lapsus/layout"
)

//go:embed dict_data/*.txt
var builtInFS embed.FS

// Dict holds word frequency dictionaries for EN and RU.
type Dict struct {
	en map[string]int
	ru map[string]int
}

// New creates a dictionary loaded from embedded frequency files.
func New() *Dict {
	d := &Dict{
		en: make(map[string]int),
		ru: make(map[string]int),
	}
	d.loadEmbedded()
	return d
}

func (d *Dict) loadEmbedded() {
	entries, err := builtInFS.ReadDir("dict_data")
	if err != nil {
		panic("embedded dict_data directory not found: " + err.Error())
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := builtInFS.ReadFile("dict_data/" + entry.Name())
		if err != nil {
			panic("failed to read embedded file " + entry.Name() + ": " + err.Error())
		}
		d.parseFile(strings.NewReader(string(data)), entry.Name())
	}
}

func (d *Dict) parseFile(r *strings.Reader, filename string) {
	var dict map[string]int
	switch {
	case strings.HasSuffix(filename, "en_freq.txt"):
		dict = d.en
	case strings.HasSuffix(filename, "ru_freq.txt"):
		dict = d.ru
	default:
		return
	}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		word := layout.NormalizeLower(parts[0])
		score, err := strconv.Atoi(parts[1])
		if err != nil {
			score = 1
		}
		dict[word] = score
	}
}

// LoadUserDict loads user dictionary files from the given directory.
// Missing directory is not an error. User entries override built-in ones.
func (d *Dict) LoadUserDict(dir string) error {
	expanded, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(expanded)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var readErrs []error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(expanded, entry.Name()))
		if err != nil {
			log.Printf("warning: skipping %s: %v", entry.Name(), err)
			readErrs = append(readErrs, err)
			continue
		}
		d.parseFile(strings.NewReader(string(data)), entry.Name())
	}
	if len(readErrs) > 0 {
		return readErrs[0]
	}
	return nil
}

func (d *Dict) selectDict(l layout.Layout) map[string]int {
	switch l {
	case layout.LayoutEN:
		return d.en
	case layout.LayoutRU:
		return d.ru
	}
	return nil
}

// Contains checks if a word exists in the appropriate dictionary for the given layout.
func (d *Dict) Contains(word string, l layout.Layout) bool {
	dict := d.selectDict(l)
	if dict == nil {
		return false
	}
	_, ok := dict[layout.NormalizeLower(word)]
	return ok
}

// Score returns the frequency score of a word in the given layout's dictionary.
func (d *Dict) Score(word string, l layout.Layout) int {
	dict := d.selectDict(l)
	if dict == nil {
		return 0
	}
	return dict[layout.NormalizeLower(word)]
}

// Count returns the number of words in the dictionary for the given layout.
func (d *Dict) Count(l layout.Layout) int {
	if dict := d.selectDict(l); dict != nil {
		return len(dict)
	}
	return 0
}
