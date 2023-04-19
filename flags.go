package drivelist

import (
	"flag"
	"strings"
)

type fieldFlags []string

func (f *fieldFlags) String() string {
	return strings.Join(*f, ",")
}
func (f *fieldFlags) Set(value string) error {
	*f = []string{}
	newFields := strings.Split(value, ",")
	for _, nf := range newFields {
		// Check if it's a valid field name

		// Add to the list
		*f = append(*f, nf)
	}
	return nil
}

var (
	Fields = []string{
		"devicename",
		"devices",
		"wwn",
		"syspath",
		"model",
		"serial",
		//"attribs",
		"uses",
		"genericdevice",
		"expander",
		"expanderpath",
		"bay",
		"size",
	}
	FieldFlag fieldFlags
)

func init() {
	FieldFlag = append(FieldFlag, "devicename", "model", "wwn", "serial", "expander", "bay", "size")
	flag.Var(&FieldFlag, "fields", "Default fields")
}
