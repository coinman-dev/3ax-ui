// Package reflect_util provides reflection utilities for working with struct fields and values.
package reflect_util

import "reflect"

// GetFields returns all struct fields of the given reflect.Type.
func GetFields(t reflect.Type) []reflect.StructField {
	num := t.NumField()
	fields := make([]reflect.StructField, 0, num)
	for i := range num {
		fields = append(fields, t.Field(i))
	}
	return fields
}
