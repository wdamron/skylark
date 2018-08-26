// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package skylark

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"unsafe"

	"github.com/google/skylark/internal/compile"
	"github.com/google/skylark/syntax"
)

// See https://ep2013.europython.eu/media/conference/slides/advanced-pickling-with-stackless-python-and-spickle.pdf

const (
	T_Encoded_End    = 1
	T_Toplevel       = 2
	T_Toplevel_End   = 3
	T_FnShared       = 4
	T_FnShared_End   = 5
	T_Frame          = 6
	T_Frame_End      = 7
	T_None           = 8
	T_True           = 9
	T_False          = 10
	T_Int            = 11
	T_Float          = 12
	T_String         = 13
	T_StringIterable = 14
	T_StringIterator = 15
	T_Function       = 16
	T_Builtin        = 17
	T_List           = 18
	T_ListIterator   = 19
	T_Dict           = 20
	T_KeyIterator    = 21
	T_Set            = 22
	T_Tuple          = 23
	T_TupleIterator  = 24
	T_Range          = 25
	T_RangeIterator  = 26
	T_Ref            = 27
	T_Funcode        = 28
	T_Custom         = 29
	T_CustomIterator = 30

	T_Uncompressed      = 60
	T_HuffmanCompressed = 61
)

var (
	ErrShortBuffer = errors.New("Codec: reached end of buffer while decoding")
	ErrBadTag      = errors.New("Codec: invalid tag while decoding")
	ErrBadRef      = errors.New("Codec: invalid ref while decoding")
)

type CustomDecoder func(dec *Decoder) (Value, error)

var CustomDecoders = map[string]CustomDecoder{}

type CustomIteratorDecoder func(dec *Decoder) (Iterator, error)

var CustomIteratorDecoders = map[string]CustomIteratorDecoder{}

type Codable interface {
	Type() string
	Encode(*Encoder)
}

func RegisterDecoder(typeName string, dec CustomDecoder) error {
	if CustomDecoders[typeName] != nil {
		return fmt.Errorf("Codec: decoder for type %s is already registered", typeName)
	}
	CustomDecoders[typeName] = dec
	return nil
}

func RegisterIteratorDecoder(typeName string, dec CustomIteratorDecoder) error {
	if CustomIteratorDecoders[typeName] != nil {
		return fmt.Errorf("Codec: iterator decoder for type %s is already registered", typeName)
	}
	CustomIteratorDecoders[typeName] = dec
	return nil
}

type ref uint32

type taggedRef struct {
	ref uint32
	tag byte
	_   [3]byte
}

type Encoder struct {
	strings     map[string]ref
	dicts       map[*hashtable]taggedRef
	lists       map[*List]ref
	tuples      map[reflect.SliceHeader]ref
	funcs       map[*Function]ref
	funcodes    map[*compile.Funcode]ref
	buf         bytes.Buffer
	compression byte
}

type Decoder struct {
	Data        []byte             // remaining encoded data
	values      []Value            // decoded values
	funcodes    []*compile.Funcode // decoded compiled functions
	prog        *compile.Program   // decoded top-level program
	predeclared StringDict         // decoded predeclared values
	globals     []Value            // decoded globals
	constants   []Value            // decoded constants
}

func NewEncoder() *Encoder {
	return &Encoder{compression: T_HuffmanCompressed}
}

func NewDecoder(data []byte, predeclared StringDict) *Decoder {
	return &Decoder{Data: data, predeclared: predeclared}
}

func (enc *Encoder) Bytes() []byte {
	return enc.buf.Bytes()
}

func (enc *Encoder) BufferSize() int {
	return enc.buf.Cap()
}

func (enc *Encoder) EnableHuffmanCompression() *Encoder {
	enc.compression = T_HuffmanCompressed
	return enc
}

func (enc *Encoder) DisableCompression() *Encoder {
	enc.compression = T_Uncompressed
	return enc
}

func (enc *Encoder) Reset() *Encoder {
	enc.strings, enc.dicts, enc.lists, enc.tuples, enc.funcs, enc.funcodes = nil, nil, nil, nil, nil, nil
	enc.buf.Reset()
	return enc
}

func (dec *Decoder) Remaining() int {
	return len(dec.Data)
}

func (dec *Decoder) Program() *compile.Program {
	return dec.prog
}

func (dec *Decoder) FnShared() (StringDict, []Value, []Value) {
	return dec.predeclared, dec.globals, dec.constants
}

func (dec *Decoder) Reset(data []byte) {
	dec.Data, dec.values, dec.funcodes, dec.prog, dec.predeclared, dec.globals, dec.constants = data, nil, nil, nil, nil, nil, nil
}

func (enc *Encoder) WriteTag(tag byte) {
	enc.buf.WriteByte(tag)
}

func (enc *Encoder) WriteUvarint(n uint64) {
	var b [8]byte
	nw := binary.PutUvarint(b[:8], n)
	enc.buf.Write(b[:nw])
}

func (dec *Decoder) DecodeUvarint() (uint64, error) {
	if dec.Remaining() == 0 {
		return 0, ErrShortBuffer
	}
	n, size := binary.Uvarint(dec.Data)
	dec.Data = dec.Data[size:]
	return n, nil
}

func (enc *Encoder) WriteVarint(n int64) {
	var b [8]byte
	nw := binary.PutVarint(b[:8], n)
	enc.buf.Write(b[:nw])
}

func (dec *Decoder) DecodeVarint() (int64, error) {
	if dec.Remaining() == 0 {
		return 0, ErrShortBuffer
	}
	n, size := binary.Varint(dec.Data)
	dec.Data = dec.Data[size:]
	return n, nil
}

func (enc *Encoder) nextRef() ref {
	return ref(len(enc.strings) + len(enc.dicts) + len(enc.lists) + len(enc.tuples) + len(enc.funcs))
}

func (dec *Decoder) GetRef(tag byte) (Value, error) {
	t, r, err := dec.DecodeRef()
	if err != nil {
		return nil, err
	}
	if t != tag {
		return nil, fmt.Errorf("Codec: unexpected tag (%v) while decoding ref to %v", t, tag)
	}
	if int(r) >= len(dec.values) {
		return nil, fmt.Errorf("Codec: out of bounds reference; ref=%v tag=%v refs=%v", r, tag, len(dec.values))
	}
	return dec.values[r], nil
}

func (enc *Encoder) EncodeRef(tag byte, r ref) {
	enc.WriteTag(T_Ref)
	enc.WriteTag(tag)
	enc.WriteUvarint(uint64(r))
}

func (dec *Decoder) DecodeRef() (byte, ref, error) {
	if dec.Remaining() < 3 {
		return 0, ref(0), ErrShortBuffer
	}
	tag, subtag := dec.Data[0], dec.Data[1]
	if tag != T_Ref {
		return 0, ref(0), fmt.Errorf("Codec: unexpected tag (%v) while decoding ref", tag)
	}
	switch subtag {
	case T_String, T_List, T_Dict, T_Set, T_Tuple, T_Function, T_Funcode:
	default:
		return 0, ref(0), fmt.Errorf("Codec: unexpected reference tag (%v) while decoding ref", subtag)
	}
	dec.Data = dec.Data[2:]
	n, err := dec.DecodeUvarint()
	return subtag, ref(n), err
}

func (enc *Encoder) EncodePosition(pos syntax.Position) {
	enc.EncodeString(String(pos.Filename()))
	enc.WriteUvarint(uint64(uint32(pos.Line)))
	enc.WriteUvarint(uint64(uint32(pos.Col)))
}

func (dec *Decoder) DecodePosition() (syntax.Position, error) {
	f, err := dec.DecodeString()
	if err != nil {
		return syntax.Position{}, fmt.Errorf("Codec: unexpected error while decoding position: %v", err)
	}
	filename := string(f)
	var line, col uint64
	line, err = dec.DecodeUvarint()
	if err != nil {
		return syntax.Position{}, fmt.Errorf("Codec: unexpected error while decoding position: %v", err)
	}
	col, err = dec.DecodeUvarint()
	if err != nil {
		return syntax.Position{}, fmt.Errorf("Codec: unexpected error while decoding position: %v", err)
	}
	return syntax.MakePosition(&filename, int32(line), int32(col)), nil
}

func (enc *Encoder) EncodeIdent(id compile.Ident) {
	enc.EncodeString(String(id.Name))
	enc.EncodePosition(id.Pos)
}

func (dec *Decoder) DecodeIdent() (compile.Ident, error) {
	name, err := dec.DecodeString()
	if err != nil {
		return compile.Ident{Name: string(name)}, fmt.Errorf("Codec: unexpected error while decoding ident: %v", err)
	}
	var pos syntax.Position
	pos, err = dec.DecodePosition()
	if err != nil {
		err = fmt.Errorf("Codec: unexpected error while decoding ident: %v", err)
	}
	return compile.Ident{string(name), pos}, err
}

func (enc *Encoder) EncodeFuncode(fc *compile.Funcode) {
	if r, ok := enc.funcodes[fc]; ok {
		enc.EncodeRef(T_Funcode, r)
		return
	}
	enc.WriteTag(T_Funcode)
	enc.EncodePosition(fc.Pos)
	enc.EncodeString(String(fc.Name))
	enc.WriteUvarint(uint64(len(fc.Code)))
	enc.buf.Write(fc.Code)
	pcline := fc.PCLineTable()
	enc.WriteUvarint(uint64(len(pcline)))
	for _, x := range pcline {
		enc.WriteUvarint(uint64(x))
	}
	enc.WriteUvarint(uint64(len(fc.Locals)))
	for _, id := range fc.Locals {
		enc.EncodeIdent(id)
	}
	enc.WriteUvarint(uint64(len(fc.Freevars)))
	for _, id := range fc.Freevars {
		enc.EncodeIdent(id)
	}
	enc.WriteUvarint(uint64(fc.MaxStack))
	enc.WriteUvarint(uint64(fc.NumParams))
	enc.EncodeBool(Bool(fc.HasVarargs))
	enc.EncodeBool(Bool(fc.HasKwargs))
	if len(enc.funcodes) == 0 {
		enc.funcodes = make(map[*compile.Funcode]ref)
	}
	enc.funcodes[fc] = ref(len(enc.funcodes))
}

func (dec *Decoder) DecodeFuncode() (*compile.Funcode, error) {
	if dec.Remaining() < 3 {
		return nil, ErrShortBuffer
	}
	if dec.Data[0] == T_Ref {
		tag, r, err := dec.DecodeRef()
		if err != nil {
			return nil, fmt.Errorf("Codec: unexpected error while decoding funcode: %v", err)
		}
		if tag != T_Funcode {
			return nil, fmt.Errorf("Codec: unexpected reference tag (%v) while decoding funcode ref", tag)
		}
		if int(r) >= len(dec.funcodes) {
			return nil, fmt.Errorf("Codec: unexpected error while decoding funcode: %v", ErrBadRef)
		}
		return dec.funcodes[r], nil
	}
	if dec.Data[0] != T_Funcode {
		return nil, fmt.Errorf("Codec: unexpected tag (%v) while decoding funcode", dec.Data[0])
	}
	dec.Data = dec.Data[1:]
	var err error
	fc := &compile.Funcode{}
	fc.Pos, err = dec.DecodePosition()
	if err != nil {
		return fc, fmt.Errorf("Codec: unexpected error while decoding funcode: %v", err)
	}
	var name String
	name, err = dec.DecodeString()
	if err != nil {
		return fc, fmt.Errorf("Codec: unexpected error while decoding funcode: %v", err)
	}
	fc.Name = string(name)
	var count uint64
	count, err = dec.DecodeUvarint()
	if err != nil {
		return fc, fmt.Errorf("Codec: unexpected error while decoding funcode: %v", err)
	}
	if int(count) > dec.Remaining() {
		return fc, ErrShortBuffer
	}
	code := make([]byte, count)
	copy(code, dec.Data)
	fc.Code = code
	dec.Data = dec.Data[count:]
	count, err = dec.DecodeUvarint()
	if err != nil {
		return fc, fmt.Errorf("Codec: unexpected error while decoding funcode: %v", err)
	}
	pcline := make([]uint16, count)
	for i := uint64(0); i < count; i++ {
		var x uint64
		x, err = dec.DecodeUvarint()
		if err != nil {
			return fc, fmt.Errorf("Codec: unexpected error while decoding funcode: %v", err)
		}
		pcline[i] = uint16(x)
	}
	fc.SetPCLineTable(pcline)
	count, err = dec.DecodeUvarint()
	if err != nil {
		return fc, fmt.Errorf("Codec: unexpected error while decoding funcode: %v", err)
	}
	fc.Locals = make([]compile.Ident, count)
	for i := uint64(0); i < count; i++ {
		fc.Locals[i], err = dec.DecodeIdent()
		if err != nil {
			return fc, fmt.Errorf("Codec: unexpected error while decoding funcode: %v", err)
		}
	}
	count, err = dec.DecodeUvarint()
	if err != nil {
		return fc, fmt.Errorf("Codec: unexpected error while decoding funcode: %v", err)
	}
	fc.Freevars = make([]compile.Ident, count)
	for i := uint64(0); i < count; i++ {
		fc.Freevars[i], err = dec.DecodeIdent()
		if err != nil {
			return fc, fmt.Errorf("Codec: unexpected error while decoding funcode: %v", err)
		}
	}
	var maxstack, numparams uint64
	maxstack, err = dec.DecodeUvarint()
	if err != nil {
		return fc, fmt.Errorf("Codec: unexpected error while decoding funcode: %v", err)
	}
	numparams, err = dec.DecodeUvarint()
	if err != nil {
		return fc, fmt.Errorf("Codec: unexpected error while decoding funcode: %v", err)
	}
	fc.MaxStack, fc.NumParams = int(maxstack), int(numparams)
	var hasvargs, haskwargs Bool
	hasvargs, err = dec.DecodeBool()
	if err != nil {
		return fc, fmt.Errorf("Codec: unexpected error while decoding funcode: %v", err)
	}
	haskwargs, err = dec.DecodeBool()
	if err != nil {
		return fc, fmt.Errorf("Codec: unexpected error while decoding funcode: %v", err)
	}
	dec.funcodes = append(dec.funcodes, fc)
	fc.HasVarargs, fc.HasKwargs = bool(hasvargs), bool(haskwargs)
	return fc, nil
}

func (enc *Encoder) EncodeValue(v Value) {
	switch t := v.(type) {
	case Bool:
		enc.EncodeBool(t)
	case Int:
		enc.EncodeInt(t)
	case Float:
		enc.EncodeFloat(t)
	case String:
		enc.EncodeString(t)
	case *Dict:
		enc.EncodeDict(t)
	case *List:
		enc.EncodeList(t)
	case *Set:
		enc.EncodeSet(t)
	case Tuple:
		enc.EncodeTuple(t)
	case *Function:
		enc.EncodeFunction(t)
	case *Builtin:
		enc.EncodeBuiltin(t)
	case rangeValue:
		enc.EncodeRange(t)
	case Codable:
		enc.WriteTag(T_Custom)
		enc.EncodeString(String(t.Type()))
		t.Encode(enc)
	default:
		enc.WriteTag(T_None)
	}
}

func (dec *Decoder) DecodeValue() (Value, error) {
	if dec.Remaining() < 2 {
		return nil, ErrShortBuffer
	}
	tag := dec.Data[0]
	switch tag {
	case T_None:
		dec.Data = dec.Data[1:]
		return None, nil
	case T_True, T_False:
		return dec.DecodeBool()
	case T_Int:
		return dec.DecodeInt()
	case T_Float:
		return dec.DecodeFloat()
	case T_String:
		return dec.DecodeString()
	case T_StringIterable:
		return dec.DecodeStringIterable()
	case T_Function:
		return dec.DecodeFunction()
	case T_Builtin:
		return dec.DecodeBuiltin()
	case T_List:
		return dec.DecodeList()
	case T_Dict:
		return dec.DecodeDict()
	case T_Set:
		return dec.DecodeSet()
	case T_Tuple:
		return dec.DecodeTuple()
	case T_Range:
		return dec.DecodeRange()
	case T_Ref:
		return dec.GetRef(dec.Data[1])
	case T_Custom:
		dec.Data = dec.Data[1:]
		typeName, err := dec.DecodeString()
		if err != nil {
			return nil, fmt.Errorf("Codec: missing type name for custom type decoder: %v", err)
		}
		if custom := CustomDecoders[string(typeName)]; custom != nil {
			return custom(dec)
		}
		return nil, fmt.Errorf("Codec: missing custom decoder for type %s", string(typeName))
	default:
		return nil, fmt.Errorf("Codec: unexpected tag (%v) while decoding value", tag)
	}
}

func (enc *Encoder) EncodeBool(b Bool) {
	if b {
		enc.WriteTag(T_True)
	} else {
		enc.WriteTag(T_False)
	}
}

func (dec *Decoder) DecodeBool() (Bool, error) {
	if dec.Remaining() == 0 {
		return False, ErrShortBuffer
	}
	tag := dec.Data[0]
	dec.Data = dec.Data[1:]
	switch tag {
	case T_True:
		return True, nil
	case T_False:
		return False, nil
	default:
		return False, fmt.Errorf("Codec: unexpected tag (%v) while decoding bool", tag)
	}
}

func (enc *Encoder) EncodeInt(i Int) {
	raw := i.bigint.Bytes()
	enc.WriteTag(T_Int)
	enc.WriteUvarint(uint64(len(raw)))
	enc.buf.Write(raw)
}

func (dec *Decoder) DecodeInt() (Int, error) {
	if dec.Remaining() < 2 {
		return Int{}, ErrShortBuffer
	}
	tag := dec.Data[0]
	if tag != T_Int {
		return Int{}, fmt.Errorf("Codec: unexpected tag (%v) while decoding int", tag)
	}
	dec.Data = dec.Data[1:]
	size, err := dec.DecodeUvarint()
	if err != nil {
		return Int{}, fmt.Errorf("Codec: unexpected error while decoding int: %v", err)
	}
	if int(size) > dec.Remaining() {
		return Int{}, ErrShortBuffer
	}
	raw := make([]byte, size)
	copy(raw, dec.Data)
	dec.Data = dec.Data[size:]
	return Int{bigint: big.NewInt(0).SetBytes(raw)}, nil
}

func (enc *Encoder) EncodeFloat(f Float) {
	enc.WriteTag(T_Float)
	enc.WriteUvarint(math.Float64bits(float64(f)))
}

func (dec *Decoder) DecodeFloat() (Float, error) {
	if dec.Remaining() < 2 {
		return Float(0.0), ErrShortBuffer
	}
	tag := dec.Data[0]
	if tag != T_Float {
		return Float(0.0), fmt.Errorf("Codec: unexpected tag (%v) while decoding float", tag)
	}
	u, err := dec.DecodeUvarint()
	if err != nil {
		return Float(0.0), fmt.Errorf("Codec: unexpected error while decoding float: %v", err)
	}
	return Float(math.Float64frombits(u)), nil
}

func (enc *Encoder) EncodeString(s String) {
	if r, ok := enc.strings[string(s)]; ok {
		enc.EncodeRef(T_String, r)
		return
	}
	enc.WriteTag(T_String)
	enc.WriteUvarint(uint64(s.Len()))
	if enc.strings == nil {
		enc.strings = make(map[string]ref)
	}
	enc.strings[string(s)] = enc.nextRef()
	enc.buf.WriteString(string(s))
}

func (dec *Decoder) DecodeString() (String, error) {
	if dec.Remaining() < 2 {
		return String(""), ErrShortBuffer
	}
	tag := dec.Data[0]
	if tag == T_Ref {
		v, err := dec.GetRef(T_String)
		if err != nil {
			return String(""), fmt.Errorf("Codec: unexpected error while decoding string: %v", err)
		}
		s, ok := v.(String)
		if !ok {
			return String(""), fmt.Errorf("Codec: unexpected error while decoding string: %v", ErrBadRef)
		}
		return s, nil
	}
	dec.Data = dec.Data[1:]
	if tag != T_String {
		return String(""), fmt.Errorf("Codec: unexpected tag (%v) while decoding string", tag)
	}
	size, err := dec.DecodeUvarint()
	if err != nil {
		return String(""), fmt.Errorf("Codec: unexpected error while decoding string: %v", err)
	}
	if dec.Remaining() < int(size) {
		return String(""), ErrShortBuffer
	}
	s := String(string(dec.Data[:size]))
	dec.values = append(dec.values, s)
	dec.Data = dec.Data[size:]
	return s, nil
}

func (enc *Encoder) EncodeFunction(fn *Function) {
	if r, ok := enc.funcs[fn]; ok {
		enc.EncodeRef(T_Function, r)
		return
	}
	enc.WriteTag(T_Function)
	enc.EncodeTuple(fn.defaults)
	enc.EncodeTuple(fn.freevars)
	enc.EncodeFuncode(fn.funcode)
	if enc.funcs == nil {
		enc.funcs = make(map[*Function]ref)
	}
	enc.funcs[fn] = enc.nextRef()
}

func (dec *Decoder) DecodeFunction() (*Function, error) {
	if dec.Remaining() < 2 {
		return nil, ErrShortBuffer
	}
	tag := dec.Data[0]
	if tag == T_Ref {
		v, err := dec.GetRef(T_Function)
		if err != nil {
			return nil, fmt.Errorf("Codec: unexpected error while decoding function: %v", err)
		}
		fn, ok := v.(*Function)
		if !ok {
			return nil, fmt.Errorf("Codec: unexpected error while decoding function: %v", ErrBadRef)
		}
		return fn, nil
	}
	dec.Data = dec.Data[1:]
	if tag != T_Function {
		return nil, fmt.Errorf("Codec: unexpected tag (%v) while decoding function", tag)
	}
	var err error
	fn := &Function{}
	fn.defaults, err = dec.DecodeTuple()
	if err != nil {
		return fn, fmt.Errorf("Codec: unexpected error while decoding function: %v", err)
	}
	fn.freevars, err = dec.DecodeTuple()
	if err != nil {
		return fn, fmt.Errorf("Codec: unexpected error while decoding function: %v", err)
	}
	fn.funcode, err = dec.DecodeFuncode()
	if err != nil {
		return fn, fmt.Errorf("Codec: unexpected error while decoding function: %v", err)
	}
	dec.values = append(dec.values, fn)
	return fn, nil
}

func (enc *Encoder) EncodeList(l *List) {
	if r, ok := enc.lists[l]; ok {
		enc.EncodeRef(T_List, r)
		return
	}
	enc.WriteTag(T_List)
	enc.EncodeBool(Bool(l.frozen))
	enc.WriteUvarint(uint64(l.Len()))
	for _, elem := range l.elems {
		enc.EncodeValue(elem)
	}
	if enc.lists == nil {
		enc.lists = make(map[*List]ref)
	}
	enc.lists[l] = enc.nextRef()
}

func (dec *Decoder) DecodeList() (*List, error) {
	if dec.Remaining() < 2 {
		return nil, ErrShortBuffer
	}
	tag := dec.Data[0]
	if tag == T_Ref {
		v, err := dec.GetRef(T_List)
		if err != nil {
			return nil, fmt.Errorf("Codec: unexpected error while decoding list: %v", err)
		}
		l, ok := v.(*List)
		if !ok {
			return nil, fmt.Errorf("Codec: unexpected error while decoding tuple: %v", ErrBadRef)
		}
		return l, nil
	}
	dec.Data = dec.Data[1:]
	if tag != T_List {
		return nil, fmt.Errorf("Codec: unexpected tag (%v) while decoding list", tag)
	}
	frozen, err := dec.DecodeBool()
	if err != nil {
		return nil, fmt.Errorf("Codec: unexpected error while decoding list: %v", err)
	}
	var size uint64
	size, err = dec.DecodeUvarint()
	if err != nil {
		return nil, fmt.Errorf("Codec: unexpected error while decoding list: %v", err)
	}
	elems := make([]Value, size)
	for i := uint64(0); i < size; i++ {
		elems[i], err = dec.DecodeValue()
		if err != nil {
			return nil, fmt.Errorf("Codec: unexpected error while decoding list: %v", err)
		}
	}
	l := NewList(elems)
	if frozen {
		l.Freeze()
	}
	dec.values = append(dec.values, l)
	return l, nil
}

func (enc *Encoder) EncodeDict(d *Dict) {
	if r, ok := enc.dicts[&d.ht]; ok {
		enc.EncodeRef(T_Dict, ref(r.ref))
		return
	}
	enc.WriteTag(T_Dict)
	enc.EncodeBool(Bool(d.ht.frozen))
	items := d.Items()
	enc.WriteUvarint(uint64(len(items)))
	for _, pair := range items {
		enc.EncodeValue(pair[0])
		enc.EncodeValue(pair[1])
	}
	if enc.dicts == nil {
		enc.dicts = make(map[*hashtable]taggedRef)
	}
	enc.dicts[&d.ht] = taggedRef{ref: uint32(enc.nextRef()), tag: T_Dict}
}

func (dec *Decoder) DecodeDict() (*Dict, error) {
	if dec.Remaining() < 2 {
		return nil, ErrShortBuffer
	}
	tag := dec.Data[0]
	if tag == T_Ref {
		v, err := dec.GetRef(T_Dict)
		if err != nil {
			return nil, fmt.Errorf("Codec: unexpected error while decoding dict: %v", err)
		}
		d, ok := v.(*Dict)
		if !ok {
			return nil, fmt.Errorf("Codec: unexpected error while decoding dict: %v", ErrBadRef)
		}
		return d, nil
	}
	dec.Data = dec.Data[1:]
	if tag != T_Dict {
		return nil, fmt.Errorf("Codec: unexpected tag (%v) while decoding dict", tag)
	}
	frozen, err := dec.DecodeBool()
	if err != nil {
		return nil, fmt.Errorf("Codec: unexpected error while decoding dict: %v", err)
	}
	size, err := dec.DecodeUvarint()
	if err != nil {
		return nil, fmt.Errorf("Codec: unexpected error while decoding dict: %v", err)
	}
	d := &Dict{}
	for i := uint64(0); i < size; i++ {
		var k Value
		k, err = dec.DecodeValue()
		if err != nil {
			return nil, fmt.Errorf("Codec: unexpected error while decoding dict: %v", err)
		}
		var v Value
		v, err = dec.DecodeValue()
		if err != nil {
			return nil, fmt.Errorf("Codec: unexpected error while decoding dict: %v", err)
		}
		if err = d.Set(k, v); err != nil {
			return nil, fmt.Errorf("Codec: unexpected error while decoding dict: %v", err)
		}
	}
	if frozen {
		d.Freeze()
	}
	dec.values = append(dec.values, d)
	return d, nil
}

func (enc *Encoder) EncodeSet(s *Set) {
	if r, ok := enc.dicts[&s.ht]; ok {
		enc.EncodeRef(T_Set, ref(r.ref))
		return
	}
	enc.WriteTag(T_Set)
	enc.EncodeBool(Bool(s.ht.frozen))
	elems := s.elems()
	enc.WriteUvarint(uint64(len(elems)))
	for _, elem := range elems {
		enc.EncodeValue(elem)
	}
	if enc.dicts == nil {
		enc.dicts = make(map[*hashtable]taggedRef)
	}
	enc.dicts[&s.ht] = taggedRef{ref: uint32(enc.nextRef()), tag: T_Set}
}

func (dec *Decoder) DecodeSet() (*Set, error) {
	if dec.Remaining() < 2 {
		return nil, ErrShortBuffer
	}
	tag := dec.Data[0]
	if tag == T_Ref {
		v, err := dec.GetRef(T_Set)
		if err != nil {
			return nil, fmt.Errorf("Codec: unexpected error while decoding set: %v", err)
		}
		s, ok := v.(*Set)
		if !ok {
			return nil, fmt.Errorf("Codec: unexpected error while decoding set: %v", ErrBadRef)
		}
		return s, nil
	}
	dec.Data = dec.Data[1:]
	if tag != T_Set {
		return nil, fmt.Errorf("Codec: unexpected tag (%v) while decoding set", tag)
	}
	frozen, err := dec.DecodeBool()
	if err != nil {
		return nil, fmt.Errorf("Codec: unexpected error while decoding set: %v", err)
	}
	var size uint64
	size, err = dec.DecodeUvarint()
	if err != nil {
		return nil, fmt.Errorf("Codec: unexpected error while decoding set: %v", err)
	}
	s := &Set{}
	for i := uint64(0); i < size; i++ {
		var v Value
		v, err = dec.DecodeValue()
		if err != nil {
			return nil, fmt.Errorf("Codec: unexpected error while decoding set: %v", err)
		}
		if err = s.Insert(v); err != nil {
			return s, fmt.Errorf("Codec: unexpected error while decoding set: %v", err)
		}
	}
	if frozen {
		s.Freeze()
	}
	dec.values = append(dec.values, s)
	return s, nil
}

func (enc *Encoder) EncodeTuple(t Tuple) {
	h := *(*reflect.SliceHeader)(unsafe.Pointer(&t))
	if r, ok := enc.tuples[h]; ok {
		enc.EncodeRef(T_Tuple, r)
		return
	}
	enc.WriteTag(T_Tuple)
	enc.WriteUvarint(uint64(len(t)))
	for _, elem := range t {
		enc.EncodeValue(elem)
	}
	if enc.tuples == nil {
		enc.tuples = make(map[reflect.SliceHeader]ref)
	}
	enc.tuples[h] = enc.nextRef()
}

func (dec *Decoder) DecodeTuple() (Tuple, error) {
	if dec.Remaining() < 2 {
		return nil, ErrShortBuffer
	}
	tag := dec.Data[0]
	if tag == T_Ref {
		v, err := dec.GetRef(T_Tuple)
		if err != nil {
			return nil, fmt.Errorf("Codec: unexpected error while decoding tuple: %v", err)
		}
		t, ok := v.(Tuple)
		if !ok {
			return nil, fmt.Errorf("Codec: unexpected error while decoding tuple: %v", ErrBadRef)
		}
		return t, nil
	}
	dec.Data = dec.Data[1:]
	if tag != T_Tuple {
		return nil, fmt.Errorf("Codec: unexpected tag (%v) while decoding tuple", tag)
	}
	size, err := dec.DecodeUvarint()
	if err != nil {
		return nil, fmt.Errorf("Codec: unexpected error while decoding tuple: %v", err)
	}
	t := make(Tuple, size)
	for i := uint64(0); i < size; i++ {
		var v Value
		v, err = dec.DecodeValue()
		if err != nil {
			return nil, fmt.Errorf("Codec: unexpected error while decoding tuple: %v", err)
		}
		t[i] = v
	}
	dec.values = append(dec.values, t)
	return t, nil
}

func (enc *Encoder) EncodeBuiltin(b *Builtin) {
	enc.WriteTag(T_Builtin)
	enc.EncodeString(String(b.name))
	if b.recv != nil {
		enc.EncodeValue(b.recv)
	} else {
		enc.WriteTag(T_None)
	}
}

func (dec *Decoder) DecodeBuiltin() (*Builtin, error) {
	if dec.Remaining() < 2 {
		return nil, ErrShortBuffer
	}
	tag := dec.Data[0]
	dec.Data = dec.Data[1:]
	if tag != T_Builtin {
		return nil, fmt.Errorf("Codec: unexpected tag (%v) while decoding builtin", tag)
	}
	name, err := dec.DecodeString()
	if err != nil {
		return nil, fmt.Errorf("Codec: unexpected error while decoding builtin: %v", err)
	}
	var recv Value
	recv, err = dec.DecodeValue()
	if err != nil {
		return nil, fmt.Errorf("Codec: unexpected error while decoding builtin: %v", err)
	}
	if recv == nil || recv == None {
		if v, ok := Universe[string(name)]; ok {
			if builtin, ok2 := v.(*Builtin); ok2 {
				return builtin, nil
			}
			return nil, fmt.Errorf("Codec: invalid builtin retrieved; name=%s", string(name))
		}
		if v, ok := dec.predeclared[string(name)]; ok {
			if builtin, ok2 := v.(*Builtin); ok2 {
				return builtin, nil
			}
			return nil, fmt.Errorf("Codec: invalid builtin retrieved; name=%s", string(name))
		}
		return nil, fmt.Errorf("Codec: builtin not found; name=%s", string(name))
	}
	method := builtinMethodOf(recv, string(name))
	if method == nil {
		return nil, fmt.Errorf("Codec: builtin method not found; name=%s", string(name))
	}
	return method, nil
}

func (enc *Encoder) EncodeRange(r rangeValue) {
	enc.WriteTag(T_Range)
	enc.WriteVarint(int64(r.start))
	enc.WriteVarint(int64(r.stop))
	enc.WriteVarint(int64(r.step))
	enc.WriteVarint(int64(r.len))
}

func (dec *Decoder) DecodeRange() (rangeValue, error) {
	r := rangeValue{}
	if dec.Remaining() < 5 {
		return r, ErrShortBuffer
	}
	tag := dec.Data[0]
	dec.Data = dec.Data[1:]
	if tag != T_Range {
		return r, fmt.Errorf("Codec: unexpected tag (%v) while decoding range", tag)
	}
	var start, stop, step, length int64
	var err error
	start, err = dec.DecodeVarint()
	if err != nil {
		return r, fmt.Errorf("Codec: unexpected error while decoding range start: %v", err)
	}
	r.start = int(start)
	stop, err = dec.DecodeVarint()
	if err != nil {
		return r, fmt.Errorf("Codec: unexpected error while decoding range stop: %v", err)
	}
	r.stop = int(stop)
	step, err = dec.DecodeVarint()
	if err != nil {
		return r, fmt.Errorf("Codec: unexpected error while decoding range step: %v", err)
	}
	r.step = int(step)
	length, err = dec.DecodeVarint()
	if err != nil {
		return r, fmt.Errorf("Codec: unexpected error while decoding range length: %v", err)
	}
	r.len = int(length)
	return r, nil
}

func (enc *Encoder) EncodeStringIterable(it stringIterable) {
	enc.WriteTag(T_StringIterable)
	enc.EncodeString(it.s)
	enc.EncodeBool(Bool(it.codepoints))
	enc.EncodeBool(Bool(it.ords))
}

func (dec *Decoder) DecodeStringIterable() (stringIterable, error) {
	it := stringIterable{}
	if dec.Remaining() < 4 {
		return it, ErrShortBuffer
	}
	tag := dec.Data[0]
	dec.Data = dec.Data[1:]
	if tag != T_StringIterable {
		return it, fmt.Errorf("Codec: unexpected tag (%v) while decoding string iterable", tag)
	}
	var err error
	it.s, err = dec.DecodeString()
	if err != nil {
		return it, fmt.Errorf("Codec: unexpected error while decoding string iterable: %v", err)
	}
	var codepoints, ords Bool
	codepoints, err = dec.DecodeBool()
	if err != nil {
		return it, fmt.Errorf("Codec: unexpected error while decoding string iterable: %v", err)
	}
	ords, err = dec.DecodeBool()
	if err != nil {
		return it, fmt.Errorf("Codec: unexpected error while decoding string iterable: %v", err)
	}
	it.codepoints, it.ords = bool(codepoints), bool(ords)
	return it, nil
}

func (enc *Encoder) EncodeIterator(it Iterator) {
	if it == nil {
		enc.WriteTag(T_None)
	}
	switch t := it.(type) {
	case *stringIterator:
		enc.WriteTag(T_StringIterator)
		enc.EncodeStringIterable(t.si)
		enc.WriteVarint(int64(t.i))
	case *listIterator:
		enc.WriteTag(T_ListIterator)
		enc.EncodeList(t.l)
		enc.WriteVarint(int64(t.i))
	case *keyIterator:
		enc.WriteTag(T_KeyIterator)
		switch ot := t.owner.(type) {
		case *Dict:
			enc.EncodeDict(ot)
		case *Set:
			enc.EncodeSet(ot)
		}
		enc.WriteUvarint(uint64(t.offset))
	case *tupleIterator:
		enc.WriteTag(T_TupleIterator)
		enc.EncodeTuple(t.elems)
	case *rangeIterator:
		enc.WriteTag(T_RangeIterator)
		enc.EncodeRange(t.r)
		enc.WriteVarint(int64(t.i))
	case Codable:
		enc.WriteTag(T_CustomIterator)
		enc.EncodeString(String(t.Type()))
		t.Encode(enc)
	case nil:
		enc.WriteTag(T_None)
	default:
		enc.WriteTag(T_None)
	}
}

func (dec *Decoder) DecodeIterator() (Iterator, error) {
	if dec.Remaining() < 1 {
		return nil, ErrShortBuffer
	}
	var err error
	tag := dec.Data[0]
	dec.Data = dec.Data[1:]
	switch tag {
	case T_None:
		return nil, nil
	case T_StringIterator:
		it := stringIterator{}
		it.si, err = dec.DecodeStringIterable()
		if err != nil {
			return &it, fmt.Errorf("Codec: unexpected error while decoding string iterator: %v", err)
		}
		var i int64
		i, err := dec.DecodeVarint()
		if err != nil {
			return &it, fmt.Errorf("Codec: unexpected error while decoding string iterator: %v", err)
		}
		it.i = int(i)
		return &it, nil
	case T_ListIterator:
		it := listIterator{}
		it.l, err = dec.DecodeList()
		if err != nil {
			return &it, fmt.Errorf("Codec: unexpected error while decoding list iterator: %v", err)
		}
		var i int64
		i, err := dec.DecodeVarint()
		if err != nil {
			return &it, fmt.Errorf("Codec: unexpected error while decoding list iterator: %v", err)
		}
		it.i = int(i)
		return &it, nil
	case T_KeyIterator:
		it := keyIterator{}
		switch dec.Data[0] {
		case T_Dict:
			var d *Dict
			d, err = dec.DecodeDict()
			if err != nil {
				return &it, fmt.Errorf("Codec: unexpected error while decoding key (dict) iterator: %v", err)
			}
			var offset uint64
			offset, err := dec.DecodeUvarint()
			if err != nil {
				return &it, fmt.Errorf("Codec: unexpected error while decoding key (dict) iterator: %v", err)
			}
			e := d.ht.head
			for i := uint64(0); i < offset && e != nil; i++ {
				e = e.next
			}
			it.e = e
			it.owner = d
			it.ht = &d.ht
			it.offset = uint(offset)
			return &it, nil
		case T_Set:
			var s *Set
			s, err = dec.DecodeSet()
			if err != nil {
				return &it, fmt.Errorf("Codec: unexpected error while decoding key (set) iterator: %v", err)
			}
			var offset uint64
			offset, err := dec.DecodeUvarint()
			if err != nil {
				return &it, fmt.Errorf("Codec: unexpected error while decoding key (set) iterator: %v", err)
			}
			e := s.ht.head
			for i := uint64(0); i < offset && e != nil; i++ {
				e = e.next
			}
			it.e = e
			it.owner = s
			it.ht = &s.ht
			it.offset = uint(offset)
			return &it, nil
		default:
			return &it, fmt.Errorf("Codec: unexpected error while decoding key iterator: %v", ErrBadTag)
		}
	case T_TupleIterator:
		it := tupleIterator{}
		it.elems, err = dec.DecodeTuple()
		return &it, fmt.Errorf("Codec: unexpected error while decoding tuple iterator: %v", err)
	case T_RangeIterator:
		it := rangeIterator{}
		it.r, err = dec.DecodeRange()
		if err != nil {
			return &it, fmt.Errorf("Codec: unexpected error while decoding range iterator: %v", err)
		}
		var i int64
		i, err = dec.DecodeVarint()
		if err != nil {
			return &it, fmt.Errorf("Codec: unexpected error while decoding range iterator: %v", err)
		}
		it.i = int(i)
		return &it, err
	case T_CustomIterator:
		typeName, err := dec.DecodeString()
		if err != nil {
			return nil, fmt.Errorf("Codec: missing type name while decoding custom iterator: %v", err)
		}
		if custom := CustomIteratorDecoders[string(typeName)]; custom != nil {
			return custom(dec)
		}
		return nil, fmt.Errorf("Codec: missing custom iterator decoder for type %s", string(typeName))
	default:
		return nil, fmt.Errorf("Codec: unexpected tag (%v) while decoding iterator", tag)
	}
}
