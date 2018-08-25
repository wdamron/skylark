// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package skylarkstruct

import (
	"fmt"

	"github.com/google/skylark"
)

func init() {
	skylark.RegisterDecoder("struct", DecodeStruct)
}

func (s *Struct) Encode(enc *skylark.Encoder) {
	enc.EncodeValue(s.constructor)
	enc.WriteUvarint(uint64(len(s.entries)))
	for _, entry := range s.entries {
		enc.EncodeString(skylark.String(entry.name))
		enc.EncodeValue(entry.value)
	}
}

func DecodeStruct(dec *skylark.Decoder) (skylark.Value, error) {
	s := &Struct{}
	v, err := dec.DecodeValue()
	if err != nil {
		return nil, fmt.Errorf("Struct codec: error decoding constructor value: %v", err)
	}
	s.constructor = v
	var count uint64
	count, err = dec.DecodeUvarint()
	if err != nil {
		return s, fmt.Errorf("Struct codec: error decoding entry count: %v", err)
	}
	s.entries = make(entries, int(count))
	for i := uint64(0); i < count; i++ {
		var name skylark.String
		name, err = dec.DecodeString()
		if err != nil {
			return s, fmt.Errorf("Struct codec: error decoding entry name: %v", err)
		}
		s.entries[i].name = string(name)
		s.entries[i].value, err = dec.DecodeValue()
		if err != nil {
			return s, fmt.Errorf("Struct codec: error decoding entry value: %v", err)
		}
	}
	return s, nil
}
