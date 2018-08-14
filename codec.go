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

	"github.com/wdamron/skylark/internal/compile"
	"github.com/wdamron/skylark/syntax"
)

// See https://ep2013.europython.eu/media/conference/slides/advanced-pickling-with-stackless-python-and-spickle.pdf

const (
	T_Encoded_End = iota + 1
	T_Toplevel
	T_Toplevel_End
	T_FnShared
	T_FnShared_End
	T_Frame
	T_Frame_End
	T_None
	T_True
	T_False
	T_Int
	T_Float
	T_String
	T_StringIterable
	T_StringIterator
	T_Function
	T_Builtin
	T_List
	T_ListIterator
	T_Dict
	T_KeyIterator
	T_Set
	T_Tuple
	T_TupleIterator
	T_Range
	T_RangeIterator
	T_Ref
	T_Funcode
)

var (
	ErrShortBuffer = errors.New("Codec: reached end of buffer while decoding")
	ErrBadTag      = errors.New("Codec: invalid tag while decoding")
	ErrBadRef      = errors.New("Codec: invalid ref while decoding")
)

type CustomDecoder func(dec *Decoder) (Value, int, error)

var CustomDecoders = map[byte]CustomDecoder{}

type Codable interface {
	Encode(*Encoder)
}

type ref uint32

type taggedRef struct {
	ref uint32
	tag byte
}

type Encoder struct {
	strings  map[string]ref
	dicts    map[*hashtable]taggedRef
	lists    map[*List]ref
	tuples   map[reflect.SliceHeader]ref
	funcs    map[*Function]ref
	funcodes map[*compile.Funcode]ref
	buf      bytes.Buffer
}

type Decoder struct {
	data []byte

	// Back-references:

	values   []Value
	funcodes []*compile.Funcode

	// Function shared variables:

	prog        *compile.Program
	predeclared StringDict
	globals     []Value
	constants   []Value
}

func NewEncoder() *Encoder {
	return &Encoder{}
}

func NewDecoder(data []byte) *Decoder {
	return &Decoder{data: data}
}

func (enc *Encoder) Bytes() []byte {
	return enc.buf.Bytes()
}

func (dec *Decoder) Remaining() int {
	return len(dec.data)
}

func (dec *Decoder) Program() *compile.Program {
	return dec.prog
}

func (dec *Decoder) FnShared() (StringDict, []Value, []Value) {
	return dec.predeclared, dec.globals, dec.constants
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
	n, size := binary.Uvarint(dec.data)
	dec.data = dec.data[size:]
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
	n, size := binary.Varint(dec.data)
	dec.data = dec.data[size:]
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
	tag, subtag := dec.data[0], dec.data[1]
	if tag != T_Ref {
		return 0, ref(0), fmt.Errorf("Codec: unexpected tag (%v) while decoding ref", tag)
	}
	switch subtag {
	case T_String, T_List, T_Dict, T_Set, T_Tuple, T_Function, T_Funcode:
	default:
		return 0, ref(0), fmt.Errorf("Codec: unexpected reference tag (%v) while decoding ref", subtag)
	}
	dec.data = dec.data[2:]
	n, err := dec.DecodeUvarint()
	return subtag, ref(n), err
}

// EncodeState writes the re-entrant state of the given thread.
func (enc *Encoder) EncodeState(thread *Thread) []byte {
	enc.encodeState(thread.frame, 1, nil)
	return enc.Bytes()
}

func (enc *Encoder) encodeState(frame *Frame, count uint, anyFn *Function) {
	if anyFn == nil {
		anyFn, _ = frame.callable.(*Function)
	}
	if frame.parent != nil {
		enc.encodeState(frame.parent, count+1, anyFn)
	} else {
		enc.EncodeToplevel(anyFn.funcode.Prog)
		enc.EncodeFnShared(anyFn)
		enc.WriteUvarint(uint64(count))
	}
	enc.EncodeFrame(frame)
}

func (dec *Decoder) DecodeState() (*Thread, error) {
	if err := dec.DecodeToplevel(); err != nil {
		return nil, err
	}
	if err := dec.DecodeFnShared(); err != nil {
		return nil, err
	}
	fcount, err := dec.DecodeUvarint()
	if err != nil {
		return nil, err
	}
	var frame *Frame
	var parent *Frame
	for i := uint64(0); i < fcount; i++ {
		frame, err = dec.DecodeFrame()
		if err != nil {
			return nil, err
		}
		frame.parent = parent
		parent = frame
	}
	for _, fc := range dec.funcodes {
		fc.Prog = dec.prog
	}
	return &Thread{frame: frame}, nil
}

func (enc *Encoder) EncodeFrame(frame *Frame) {
	enc.WriteTag(T_Frame)
	enc.WriteUvarint(uint64(frame.callpc))
	enc.WriteUvarint(uint64(frame.sp))
	enc.WriteUvarint(uint64(frame.pc))
	enc.EncodePosition(frame.Position())
	enc.EncodeValue(frame.Callable().(Value))
	stack := frame.stack[:frame.sp]
	for _, v := range stack {
		if v == nil {
			enc.WriteTag(T_None)
			continue
		}
		enc.EncodeValue(v)
	}
	enc.WriteUvarint(uint64(len(frame.iterstack)))
	for _, v := range frame.iterstack {
		if v == nil {
			enc.WriteTag(T_None)
			continue
		}
		enc.EncodeIterator(v)
	}
	enc.WriteTag(T_Frame_End)
}

func (dec *Decoder) DecodeFrame() (*Frame, error) {
	if dec.Remaining() < 2 {
		return nil, ErrShortBuffer
	}
	tag := dec.data[0]
	dec.data = dec.data[1:]
	if tag != T_Frame {
		return nil, fmt.Errorf("Codec: unexpected tag (%v) while decoding frame", tag)
	}
	frame := &Frame{}
	var x uint64
	var err error
	// callpc
	x, err = dec.DecodeUvarint()
	if err != nil {
		return frame, fmt.Errorf("Codec: unexpected error while decoding frame: %v", err)
	}
	frame.callpc = uint32(x)
	// sp
	x, err = dec.DecodeUvarint()
	if err != nil {
		return frame, fmt.Errorf("Codec: unexpected error while decoding frame: %v", err)
	}
	frame.sp = uint32(x)
	// pc
	x, err = dec.DecodeUvarint()
	if err != nil {
		return frame, fmt.Errorf("Codec: unexpected error while decoding frame: %v", err)
	}
	frame.pc = uint32(x)
	// posn
	frame.posn, err = dec.DecodePosition()
	if err != nil {
		return frame, fmt.Errorf("Codec: unexpected error while decoding frame: %v", err)
	}
	// callable
	var v Value
	v, err = dec.DecodeValue()
	if err != nil {
		return frame, fmt.Errorf("Codec: unexpected error while decoding frame: %v", err)
	}
	c, ok := v.(Callable)
	if !ok {
		return frame, fmt.Errorf("Codec: invalid callable while decoding frame, position: %s", frame.Position())
	}
	frame.callable = c
	// stack
	for i := 0; i < int(frame.sp); i++ {
		v, err = dec.DecodeValue()
		if err != nil {
			return frame, fmt.Errorf("Codec: unexpected error while decoding frame: %v", err)
		}
		frame.stack = append(frame.stack, v)
	}
	// iterstack
	x, err = dec.DecodeUvarint()
	if err != nil {
		return frame, fmt.Errorf("Codec: unexpected error while decoding frame: %v", err)
	}
	for i := uint64(0); i < x; i++ {
		var it Iterator
		it, _ = dec.DecodeIterator()
		if err != nil {
			return frame, fmt.Errorf("Codec: unexpected error while decoding frame: %v", err)
		}
		frame.iterstack = append(frame.iterstack, it)
	}
	if dec.data[0] != T_Frame_End {
		return frame, fmt.Errorf("Codec: unexpected end tag (%v) while decoding frame", tag)
	}
	dec.data = dec.data[1:]
	return frame, nil
}

func (enc *Encoder) EncodeToplevel(p *compile.Program) {
	enc.WriteTag(T_Toplevel)
	enc.WriteUvarint(uint64(len(p.Loads)))
	for _, load := range p.Loads {
		enc.EncodeIdent(load)
	}
	enc.WriteUvarint(uint64(len(p.Names)))
	for _, name := range p.Names {
		enc.EncodeString(String(name))
	}
	enc.WriteUvarint(uint64(len(p.Constants)))
	for _, p := range p.Constants {
		switch t := p.(type) {
		case string:
			enc.EncodeString(String(t))
		case int64:
			enc.EncodeInt(MakeInt64(t))
		case *big.Int:
			enc.EncodeInt(Int{bigint: t})
		case float64:
			enc.EncodeFloat(Float(t))
		}
	}
	enc.WriteUvarint(uint64(len(p.Functions)))
	for _, fc := range p.Functions {
		enc.EncodeFuncode(fc)
	}
	enc.WriteUvarint(uint64(len(p.Globals)))
	for _, g := range p.Globals {
		enc.EncodeIdent(g)
	}
	enc.EncodeFuncode(p.Toplevel)
	enc.WriteTag(T_Toplevel_End)
}

func (dec *Decoder) DecodeToplevel() error {
	if dec.prog != nil {
		return errors.New("Codec: toplevel already decoded")
	}
	if dec.Remaining() < 2 {
		return ErrShortBuffer
	}
	if dec.data[0] != T_Toplevel {
		return fmt.Errorf("Codec: unexpected tag (%v) while decoding top level", dec.data[0])
	}
	dec.data = dec.data[1:]
	dec.prog = &compile.Program{}

	var count uint64
	var err error
	count, err = dec.DecodeUvarint()
	if err != nil {
		return fmt.Errorf("Codec: unexpected error while decoding top level: %v", err)
	}
	dec.prog.Loads = make([]compile.Ident, int(count))
	for i := uint64(0); i < count; i++ {
		dec.prog.Loads[i], err = dec.DecodeIdent()
		if err != nil {
			return fmt.Errorf("Codec: unexpected error while decoding top level: %v", err)
		}
	}

	count, err = dec.DecodeUvarint()
	if err != nil {
		return fmt.Errorf("Codec: unexpected error while decoding top level: %v", err)
	}
	dec.prog.Names = make([]string, int(count))
	for i := uint64(0); i < count; i++ {
		var name String
		name, err = dec.DecodeString()
		if err != nil {
			return fmt.Errorf("Codec: unexpected error while decoding top level: %v", err)
		}
		dec.prog.Names[i] = string(name)
	}

	count, err = dec.DecodeUvarint()
	if err != nil {
		return fmt.Errorf("Codec: unexpected error while decoding top level: %v", err)
	}
	dec.prog.Constants = make([]interface{}, int(count))
	for i := uint64(0); i < count; i++ {
		var c Value
		c, err = dec.DecodeValue()
		if err != nil {
			return fmt.Errorf("Codec: unexpected error while decoding top level: %v", err)
		}
		switch t := c.(type) {
		case String:
			dec.prog.Constants[i] = string(t)
		case Int:
			if i64, ok := t.Int64(); ok {
				dec.prog.Constants[i] = i64
			} else {
				dec.prog.Constants[i] = t.bigint
			}
		case Float:
			dec.prog.Constants[i] = float64(t)
		}
	}

	count, err = dec.DecodeUvarint()
	if err != nil {
		return fmt.Errorf("Codec: unexpected error while decoding top level: %v", err)
	}
	dec.prog.Functions = make([]*compile.Funcode, int(count))
	for i := uint64(0); i < count; i++ {
		dec.prog.Functions[i], err = dec.DecodeFuncode()
		if err != nil {
			return fmt.Errorf("Codec: unexpected error while decoding top level: %v", err)
		}
	}

	count, err = dec.DecodeUvarint()
	if err != nil {
		return fmt.Errorf("Codec: unexpected error while decoding top level: %v", err)
	}
	dec.prog.Globals = make([]compile.Ident, int(count))
	for i := uint64(0); i < count; i++ {
		dec.prog.Globals[i], err = dec.DecodeIdent()
		if err != nil {
			return fmt.Errorf("Codec: unexpected error while decoding top level: %v", err)
		}
	}

	dec.prog.Toplevel, err = dec.DecodeFuncode()
	if err != nil {
		return fmt.Errorf("Codec: unexpected error while decoding top level: %v", err)
	}

	if dec.data[0] != T_Toplevel_End {
		return fmt.Errorf("Codec: unexpected end tag (%v) while decoding top level", dec.data[0])
	}
	dec.data = dec.data[1:]

	return nil
}

func (enc *Encoder) EncodeFnShared(fn *Function) {
	enc.WriteTag(T_FnShared)
	enc.WriteUvarint(uint64(len(fn.predeclared)))
	for k, v := range fn.predeclared {
		enc.EncodeString(String(k))
		if v == nil {
			enc.WriteTag(T_None)
			continue
		}
		enc.EncodeValue(v)
	}
	enc.WriteUvarint(uint64(len(fn.globals)))
	for _, v := range fn.globals {
		if v == nil {
			enc.WriteTag(T_None)
			continue
		}
		enc.EncodeValue(v)
	}
	enc.WriteUvarint(uint64(len(fn.constants)))
	for _, v := range fn.constants {
		if v == nil {
			enc.WriteTag(T_None)
			continue
		}
		enc.EncodeValue(v)
	}
	enc.WriteTag(T_FnShared_End)
}

func (dec *Decoder) DecodeFnShared() error {
	if len(dec.predeclared) > 0 {
		return errors.New("Codec: shared function data already decoded")
	}
	if dec.Remaining() < 2 {
		return ErrShortBuffer
	}
	if dec.data[0] != T_FnShared {
		return fmt.Errorf("Codec: unexpected tag (%v) while decoding shared sections", dec.data[0])
	}
	dec.data = dec.data[1:]
	size, err := dec.DecodeUvarint()
	if err != nil {
		return fmt.Errorf("Codec: unexpected error while decoding shared sections: %v", err)
	}
	dec.predeclared = make(StringDict, int(size))
	for i := uint64(0); i < size; i++ {
		var k String
		k, err = dec.DecodeString()
		if err != nil {
			return fmt.Errorf("Codec: unexpected error while decoding shared sections: %v", err)
		}
		var v Value
		v, err = dec.DecodeValue()
		if err != nil {
			return fmt.Errorf("Codec: unexpected error while decoding shared sections: %v", err)
		}
		dec.predeclared[string(k)] = v
	}
	size, err = dec.DecodeUvarint()
	if err != nil {
		return fmt.Errorf("Codec: unexpected error while decoding shared sections: %v", err)
	}
	dec.globals = make([]Value, int(size))
	for i := uint64(0); i < size; i++ {
		dec.globals[i], err = dec.DecodeValue()
		if err != nil {
			return fmt.Errorf("Codec: unexpected error while decoding shared sections: %v", err)
		}
	}
	size, err = dec.DecodeUvarint()
	if err != nil {
		return fmt.Errorf("Codec: unexpected error while decoding shared sections: %v", err)
	}
	dec.constants = make([]Value, int(size))
	for i := uint64(0); i < size; i++ {
		dec.constants[i], err = dec.DecodeValue()
		if err != nil {
			return fmt.Errorf("Codec: unexpected error while decoding shared sections: %v", err)
		}
	}
	if dec.data[0] != T_FnShared_End {
		return fmt.Errorf("Codec: unexpected end tag (%v) while decoding shared sections", dec.data[0])
	}
	dec.data = dec.data[1:]
	return nil
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
	if len(dec.data) < 3 {
		return nil, ErrShortBuffer
	}
	if dec.data[0] == T_Ref {
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
	if dec.data[0] != T_Funcode {
		return nil, fmt.Errorf("Codec: unexpected tag (%v) while decoding funcode", dec.data[0])
	}
	dec.data = dec.data[1:]
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
	if int(count) > len(dec.data) {
		return fc, ErrShortBuffer
	}
	code := make([]byte, int(count))
	copy(code, dec.data)
	fc.Code = code
	dec.data = dec.data[int(count):]
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
	default:
		if c, ok := v.(Codable); ok {
			c.Encode(enc)
		}
		enc.WriteTag(T_None)
	}
}

func (dec *Decoder) DecodeValue() (Value, error) {
	if len(dec.data) < 2 {
		return nil, ErrShortBuffer
	}
	tag := dec.data[0]
	switch tag {
	case T_None:
		dec.data = dec.data[1:]
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
		return dec.GetRef(dec.data[1])
	default:
		if custom, ok := CustomDecoders[tag]; ok {
			v, vlen, err := custom(dec)
			dec.data = dec.data[vlen:]
			return v, err
		}
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
	if len(dec.data) == 0 {
		return False, ErrShortBuffer
	}
	tag := dec.data[0]
	dec.data = dec.data[1:]
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
	if len(dec.data) < 2 {
		return Int{}, ErrShortBuffer
	}
	tag := dec.data[0]
	if tag != T_Int {
		return Int{}, fmt.Errorf("Codec: unexpected tag (%v) while decoding int", tag)
	}
	dec.data = dec.data[1:]
	size, err := dec.DecodeUvarint()
	if err != nil {
		return Int{}, fmt.Errorf("Codec: unexpected error while decoding int: %v", err)
	}
	if int(size) > len(dec.data) {
		return Int{}, ErrShortBuffer
	}
	raw := make([]byte, int(size))
	copy(raw, dec.data)
	dec.data = dec.data[size:]
	return Int{bigint: big.NewInt(0).SetBytes(raw)}, nil
}

func (enc *Encoder) EncodeFloat(f Float) {
	enc.WriteTag(T_Float)
	enc.WriteUvarint(math.Float64bits(float64(f)))
}

func (dec *Decoder) DecodeFloat() (Float, error) {
	if len(dec.data) < 2 {
		return Float(0.0), ErrShortBuffer
	}
	tag := dec.data[0]
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
	if len(dec.data) < 2 {
		return String(""), ErrShortBuffer
	}
	tag := dec.data[0]
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
	dec.data = dec.data[1:]
	if tag != T_String {
		return String(""), fmt.Errorf("Codec: unexpected tag (%v) while decoding string", tag)
	}
	size, err := dec.DecodeUvarint()
	if err != nil {
		return String(""), fmt.Errorf("Codec: unexpected error while decoding string: %v", err)
	}
	if len(dec.data) < int(size) {
		return String(""), ErrShortBuffer
	}
	s := String(string(dec.data[:size]))
	dec.values = append(dec.values, s)
	dec.data = dec.data[size:]
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
	if len(dec.data) < 2 {
		return nil, ErrShortBuffer
	}
	tag := dec.data[0]
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
	dec.data = dec.data[1:]
	if tag != T_Function {
		return nil, fmt.Errorf("Codec: unexpected tag (%v) while decoding function", tag)
	}
	var err error
	fn := &Function{predeclared: dec.predeclared, globals: dec.globals, constants: dec.constants}
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
	if len(dec.data) < 2 {
		return nil, ErrShortBuffer
	}
	tag := dec.data[0]
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
	dec.data = dec.data[1:]
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
	elems := make([]Value, int(size))
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
	if len(dec.data) < 2 {
		return nil, ErrShortBuffer
	}
	tag := dec.data[0]
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
	dec.data = dec.data[1:]
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
	if len(dec.data) < 2 {
		return nil, ErrShortBuffer
	}
	tag := dec.data[0]
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
	dec.data = dec.data[1:]
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
	if len(dec.data) < 2 {
		return nil, ErrShortBuffer
	}
	tag := dec.data[0]
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
	dec.data = dec.data[1:]
	if tag != T_Tuple {
		return nil, fmt.Errorf("Codec: unexpected tag (%v) while decoding tuple", tag)
	}
	size, err := dec.DecodeUvarint()
	if err != nil {
		return nil, fmt.Errorf("Codec: unexpected error while decoding tuple: %v", err)
	}
	t := make(Tuple, int(size))
	for i := uint64(0); i < size; i++ {
		var v Value
		v, err = dec.DecodeValue()
		if err != nil {
			return nil, fmt.Errorf("Codec: unexpected error while decoding tuple: %v", err)
		}
		t = append(t, v)
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
	if len(dec.data) < 2 {
		return nil, ErrShortBuffer
	}
	tag := dec.data[0]
	dec.data = dec.data[1:]
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
	// Allocate a closure over 'method'.
	impl := func(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error) {
		return method(b.Name(), b.Receiver(), args, kwargs)
	}
	return NewBuiltin(string(name), impl).BindReceiver(recv), nil
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
	tag := dec.data[0]
	dec.data = dec.data[1:]
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
	tag := dec.data[0]
	dec.data = dec.data[1:]
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

func (enc *Encoder) EncodeStringIterator(it *stringIterator) {
	enc.WriteTag(T_StringIterator)
	enc.EncodeStringIterable(it.si)
	enc.WriteVarint(int64(it.i))
}

func (enc *Encoder) EncodeListIterator(it *listIterator) {
	enc.WriteTag(T_ListIterator)
	enc.EncodeList(it.l)
	enc.WriteVarint(int64(it.i))
}

func (enc *Encoder) EncodeKeyIterator(it *keyIterator) {
	enc.WriteTag(T_KeyIterator)
	switch t := it.owner.(type) {
	case *Dict:
		enc.EncodeDict(t)
	case *Set:
		enc.EncodeSet(t)
	}
	enc.WriteUvarint(uint64(it.offset))
}

func (enc *Encoder) EncodeTupleIterator(it *tupleIterator) {
	enc.WriteTag(T_TupleIterator)
	enc.EncodeTuple(it.elems)
}

func (enc *Encoder) EncodeRangeIterator(it *rangeIterator) {
	enc.WriteTag(T_RangeIterator)
	enc.EncodeRange(it.r)
	enc.WriteVarint(int64(it.i))
}

func (enc *Encoder) EncodeIterator(it Iterator) {
	if it == nil {
		enc.WriteTag(T_None)
	}
	switch t := it.(type) {
	case *stringIterator:
		enc.EncodeStringIterator(t)
	case *listIterator:
		enc.EncodeListIterator(t)
	case *keyIterator:
		enc.EncodeKeyIterator(t)
	case *tupleIterator:
		enc.EncodeTupleIterator(t)
	case *rangeIterator:
		enc.EncodeRangeIterator(t)
	case nil:
		enc.WriteTag(T_None)
	default:
		if c, ok := it.(Codable); ok {
			c.Encode(enc)
		}
	}
}

func (dec *Decoder) DecodeIterator() (Iterator, error) {
	if dec.Remaining() < 1 {
		return nil, ErrShortBuffer
	}
	var err error
	tag := dec.data[0]
	dec.data = dec.data[1:]
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
		switch dec.data[0] {
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
	default:
		if custom, ok := CustomDecoders[tag]; ok {
			var v Value
			var vlen int
			v, vlen, err = custom(dec)
			dec.data = dec.data[vlen:]
			if err != nil {
				return nil, fmt.Errorf("Codec: unexpected error while decoding custom iterator: %v", err)
			}
			var it Iterator
			it, ok = v.(Iterator)
			if !ok {
				return nil, fmt.Errorf("Codec: unexpected error while decoding custom iterator: %v", ErrBadRef)
			}
			return it, nil
		}
		return nil, fmt.Errorf("Codec: unexpected tag (%v) while decoding iterator", tag)
	}
}
