package fs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// checkArgKeys refuses arguments whose member names the decoder would read differently from how they are written: encoding/json
// matches names without regard to case and keeps the last of two equal ones, while what shows a call to a person (the approval's
// preview) reads the exact name. So {"content": "shown", "Content": "written"} would show one write and do another. Two members of
// one object whose names differ only in case, and a member whose name matches an argument of dst only in another case, are errors.
func checkArgKeys(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return walkArgKeys(dec, reflect.TypeOf(dst), "")
}

// walkArgKeys checks the next JSON value of dec against t (nil: any value), at path.
func walkArgKeys(dec *json.Decoder, t reflect.Type, path string) error {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	tok, err := dec.Token()
	if err != nil {
		return nil // the decoder proper reports a syntax error in its own words
	}
	switch tok {
	case json.Delim('{'):
		seen := map[string]string{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil
			}
			key, _ := kt.(string)
			name := path + key
			if prev, ok := seen[strings.ToLower(key)]; ok {
				return fmt.Errorf("arguments %q and %q are the same name in two spellings: send one", path+prev, name)
			}
			seen[strings.ToLower(key)] = key
			var next reflect.Type
			if t != nil {
				switch t.Kind() {
				case reflect.Struct:
					if want, ft, ok := argField(t, key); ok {
						if want != key {
							return fmt.Errorf("argument %q is spelled %q: names are exact", path+want, name)
						}
						next = ft
					}
				case reflect.Map:
					next = t.Elem()
				}
			}
			if err := walkArgKeys(dec, next, name+"."); err != nil {
				return err
			}
		}
		_, _ = dec.Token()
	case json.Delim('['):
		var elem reflect.Type
		if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
			elem = t.Elem()
		}
		for dec.More() {
			if err := walkArgKeys(dec, elem, path); err != nil {
				return err
			}
		}
		_, _ = dec.Token()
	}
	return nil
}

// argField finds the field of struct t that the JSON member key decodes into, as encoding/json does (an exact name first, then
// one in another case), and returns that field's JSON name and type.
func argField(t reflect.Type, key string) (string, reflect.Type, bool) {
	var foldName string
	var foldType reflect.Type
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			if n, ft, ok := argField(f.Type, key); ok {
				if n == key {
					return n, ft, true
				}
				if foldName == "" {
					foldName, foldType = n, ft
				}
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		name := f.Name
		if tag, ok := f.Tag.Lookup("json"); ok {
			if tag == "-" {
				continue
			}
			if n, _, _ := strings.Cut(tag, ","); n != "" {
				name = n
			}
		}
		if name == key {
			return name, f.Type, true
		}
		if foldName == "" && strings.EqualFold(name, key) {
			foldName, foldType = name, f.Type
		}
	}
	return foldName, foldType, foldName != ""
}
