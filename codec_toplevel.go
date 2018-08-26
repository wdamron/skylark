// Copyright 2017 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package skylark

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/google/skylark/internal/compile"
)

// EncodeState encodes the re-entrant state of the given thread.
func EncodeState(thread *Thread) ([]byte, error) {
	return NewEncoder().EncodeState(thread)
}

// EncodeState decodes a re-entrant state into a resumable thread.
func DecodeState(snapshot []byte) (*Thread, error) {
	return NewDecoder(snapshot).DecodeState()
}

// EncodeState encodes the re-entrant state of the given thread.
func (enc *Encoder) EncodeState(thread *Thread) ([]byte, error) {
	if thread.SuspendedFrame() != nil {
		thread.Resumable()
	}
	err := enc.encodeState(thread.frame, 1, nil)
	if err != nil {
		return nil, err
	}

	var compressed bytes.Buffer
	compressed.WriteByte(T_HuffmanCompressed)
	var length [8]byte
	sz := binary.PutUvarint(length[:8], uint64(enc.buf.Len()))
	compressed.Write(length[:sz])
	var wr *flate.Writer
	if wr, err = flate.NewWriter(&compressed, flate.HuffmanOnly); err != nil {
		return nil, err
	}
	if _, err = wr.Write(enc.Bytes()); err != nil {
		return nil, err
	}
	if err = wr.Close(); err != nil {
		return nil, err
	}
	return compressed.Bytes(), nil
}

func (enc *Encoder) encodeState(frame *Frame, count uint, anyFn *Function) error {
	if anyFn == nil {
		anyFn, _ = frame.callable.(*Function)
	}
	if count > 1 {
		if _, isFunction := frame.callable.(*Function); !isFunction {
			return fmt.Errorf("suspended thread has non-resumable function on call-stack: %s", frame.callable.Name())
		}
	}
	if frame.parent != nil {
		if err := enc.encodeState(frame.parent, count+1, anyFn); err != nil {
			return err
		}
	} else {
		enc.EncodeToplevel(anyFn.funcode.Prog)
		enc.EncodeFnShared(anyFn)
		enc.WriteUvarint(uint64(count))
	}
	enc.EncodeFrame(frame)
	return nil
}

// EncodeState decodes a re-entrant state into a resumable thread.
func (dec *Decoder) DecodeState() (*Thread, error) {
	if dec.Remaining() < 5 {
		return nil, ErrShortBuffer
	}
	tag := dec.Data[0]
	dec.Data = dec.Data[1:]

	// Decompress the encoded state, when applicable:
	if tag == T_HuffmanCompressed {
		length, err := dec.DecodeUvarint()
		if err != nil {
			return nil, fmt.Errorf("Codec: error decoding length of compressed state: %v", err)
		}
		r := flate.NewReader(bytes.NewReader(dec.Data))
		// length+1 ensures the first read returns EOF for valid lengths:
		decompressed := make([]byte, int(length+1))
		var nr int
		nr, err = r.Read(decompressed)
		r.Close()
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("Codec: error while decompressing state: %v", err)
		}
		if err != io.EOF || nr != int(length) {
			return nil, errors.New("Codec: invalid length-prefix for compressed state")
		}
		dec.Data = decompressed[:int(length)]
	} else if tag != T_Uncompressed {
		return nil, fmt.Errorf("Codec: unrecognized compression tag (%v) in compressed state", tag)
	}

	if err := dec.DecodeToplevel(); err != nil {
		return nil, err
	}
	if err := dec.DecodeFnShared(); err != nil {
		return nil, err
	}
	var frameCount uint64
	frameCount, err := dec.DecodeUvarint()
	if err != nil {
		return nil, err
	}
	var frame *Frame
	var parent *Frame
	for i := uint64(0); i < frameCount; i++ {
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
	thread := &Thread{frame: frame}
	if dec.Remaining() > 0 {
		return thread, fmt.Errorf("Codec: %v bytes remaining after fully decoding state", dec.Remaining())
	}
	return thread, nil
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
	if dec.Data[0] != T_Toplevel {
		return fmt.Errorf("Codec: unexpected tag (%v) while decoding top level", dec.Data[0])
	}
	dec.Data = dec.Data[1:]
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

	if dec.Remaining() < 1 {
		return errors.New("Codec: missing end tag while decoding top level")
	}
	if dec.Data[0] != T_Toplevel_End {
		return fmt.Errorf("Codec: unexpected end tag (%v) while decoding top level", dec.Data[0])
	}
	dec.Data = dec.Data[1:]

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
	if dec.Data[0] != T_FnShared {
		return fmt.Errorf("Codec: unexpected tag (%v) while decoding shared sections", dec.Data[0])
	}
	dec.Data = dec.Data[1:]
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
	if dec.Remaining() < 1 {
		return errors.New("Codec: missing end tag while decoding shared sections")
	}
	if dec.Data[0] != T_FnShared_End {
		return fmt.Errorf("Codec: unexpected end tag (%v) while decoding shared sections", dec.Data[0])
	}
	dec.Data = dec.Data[1:]
	return nil
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
	tag := dec.Data[0]
	dec.Data = dec.Data[1:]
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
	if dec.Remaining() < 1 {
		return frame, errors.New("Codec: missing end tag while decoding frame")
	}
	if dec.Data[0] != T_Frame_End {
		return frame, fmt.Errorf("Codec: unexpected end tag (%v) while decoding frame", tag)
	}
	dec.Data = dec.Data[1:]
	return frame, nil
}
