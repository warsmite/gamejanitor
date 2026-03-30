package steam

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"unicode"
)

// VDFNode represents a node in a Valve Data Format (VDF/KeyValues) tree.
// Leaf nodes have Value set, branch nodes have Children set.
type VDFNode struct {
	Key      string
	Value    string
	Children []*VDFNode
}

// Child finds the first child with the given key (case-insensitive).
func (n *VDFNode) Child(key string) *VDFNode {
	lower := strings.ToLower(key)
	for _, c := range n.Children {
		if strings.ToLower(c.Key) == lower {
			return c
		}
	}
	return nil
}

// ChildValue returns the value of the first child with the given key, or empty string.
func (n *VDFNode) ChildValue(key string) string {
	c := n.Child(key)
	if c == nil {
		return ""
	}
	return c.Value
}

// Binary VDF type tags
const (
	bvdfSubTree   byte = 0x00
	bvdfString    byte = 0x01
	bvdfInt32     byte = 0x02
	bvdfFloat32   byte = 0x03
	bvdfPointer   byte = 0x04
	bvdfWideStr   byte = 0x05
	bvdfColor     byte = 0x06
	bvdfUint64    byte = 0x07
	bvdfEnd       byte = 0x08
	bvdfInt64     byte = 0x0A
)

// ParseBinaryVDF parses Valve's binary KeyValues format used in PICS responses.
func ParseBinaryVDF(data []byte) (*VDFNode, error) {
	p := &binaryVDFParser{data: data}
	root := &VDFNode{Key: "root"}
	if err := p.parseChildren(root); err != nil {
		return nil, err
	}
	return root, nil
}

type binaryVDFParser struct {
	data []byte
	pos  int
}

func (p *binaryVDFParser) parseChildren(parent *VDFNode) error {
	for p.pos < len(p.data) {
		typeByte := p.data[p.pos]
		p.pos++

		if typeByte == bvdfEnd {
			return nil
		}

		key, err := p.readCString()
		if err != nil {
			return fmt.Errorf("read key: %w", err)
		}

		switch typeByte {
		case bvdfSubTree:
			child := &VDFNode{Key: key}
			if err := p.parseChildren(child); err != nil {
				return fmt.Errorf("parse subtree %q: %w", key, err)
			}
			parent.Children = append(parent.Children, child)

		case bvdfString:
			val, err := p.readCString()
			if err != nil {
				return fmt.Errorf("read string value for %q: %w", key, err)
			}
			parent.Children = append(parent.Children, &VDFNode{Key: key, Value: val})

		case bvdfInt32:
			if p.pos+4 > len(p.data) {
				return fmt.Errorf("truncated int32 for %q", key)
			}
			v := binary.LittleEndian.Uint32(p.data[p.pos : p.pos+4])
			p.pos += 4
			parent.Children = append(parent.Children, &VDFNode{Key: key, Value: fmt.Sprintf("%d", v)})

		case bvdfUint64:
			if p.pos+8 > len(p.data) {
				return fmt.Errorf("truncated uint64 for %q", key)
			}
			v := binary.LittleEndian.Uint64(p.data[p.pos : p.pos+8])
			p.pos += 8
			parent.Children = append(parent.Children, &VDFNode{Key: key, Value: fmt.Sprintf("%d", v)})

		case bvdfInt64:
			if p.pos+8 > len(p.data) {
				return fmt.Errorf("truncated int64 for %q", key)
			}
			v := int64(binary.LittleEndian.Uint64(p.data[p.pos : p.pos+8]))
			p.pos += 8
			parent.Children = append(parent.Children, &VDFNode{Key: key, Value: fmt.Sprintf("%d", v)})

		case bvdfFloat32:
			if p.pos+4 > len(p.data) {
				return fmt.Errorf("truncated float32 for %q", key)
			}
			bits := binary.LittleEndian.Uint32(p.data[p.pos : p.pos+4])
			v := math.Float32frombits(bits)
			p.pos += 4
			parent.Children = append(parent.Children, &VDFNode{Key: key, Value: fmt.Sprintf("%f", v)})

		case bvdfColor, bvdfPointer:
			// 4 bytes, skip
			if p.pos+4 > len(p.data) {
				return fmt.Errorf("truncated %d-byte value for %q", 4, key)
			}
			v := binary.LittleEndian.Uint32(p.data[p.pos : p.pos+4])
			p.pos += 4
			parent.Children = append(parent.Children, &VDFNode{Key: key, Value: fmt.Sprintf("%d", v)})

		case bvdfWideStr:
			// Wide string — read as C string (simplified, actual is UTF-16)
			val, err := p.readCString()
			if err != nil {
				return fmt.Errorf("read wide string for %q: %w", key, err)
			}
			parent.Children = append(parent.Children, &VDFNode{Key: key, Value: val})

		default:
			return fmt.Errorf("unknown VDF type 0x%02x for key %q at offset %d", typeByte, key, p.pos-1)
		}
	}
	return nil
}

func (p *binaryVDFParser) readCString() (string, error) {
	start := p.pos
	for p.pos < len(p.data) {
		if p.data[p.pos] == 0 {
			s := string(p.data[start:p.pos])
			p.pos++ // skip null terminator
			return s, nil
		}
		p.pos++
	}
	return "", fmt.Errorf("unterminated C string at offset %d", start)
}

// ParseVDF parses a text VDF/KeyValues format string into a tree.
// Steam's some responses use this format.
func ParseVDF(input string) (*VDFNode, error) {
	p := &vdfParser{input: input}
	root := &VDFNode{Key: "root"}
	if err := p.parseChildren(root); err != nil {
		return nil, err
	}
	return root, nil
}

type vdfParser struct {
	input string
	pos   int
}

func (p *vdfParser) parseChildren(parent *VDFNode) error {
	for {
		p.skipWhitespace()
		if p.pos >= len(p.input) {
			return nil
		}

		if p.input[p.pos] == '}' {
			return nil
		}

		key, err := p.readString()
		if err != nil {
			return err
		}

		p.skipWhitespace()
		if p.pos >= len(p.input) {
			return fmt.Errorf("unexpected end of input after key %q", key)
		}

		if p.input[p.pos] == '{' {
			p.pos++ // skip {
			node := &VDFNode{Key: key}
			if err := p.parseChildren(node); err != nil {
				return err
			}
			p.skipWhitespace()
			if p.pos < len(p.input) && p.input[p.pos] == '}' {
				p.pos++ // skip }
			}
			parent.Children = append(parent.Children, node)
		} else {
			value, err := p.readString()
			if err != nil {
				return err
			}
			parent.Children = append(parent.Children, &VDFNode{Key: key, Value: value})
		}
	}
}

func (p *vdfParser) readString() (string, error) {
	p.skipWhitespace()
	if p.pos >= len(p.input) {
		return "", fmt.Errorf("unexpected end of input")
	}

	if p.input[p.pos] == '"' {
		return p.readQuotedString()
	}
	return p.readUnquotedString(), nil
}

func (p *vdfParser) readQuotedString() (string, error) {
	p.pos++ // skip opening "
	var b strings.Builder
	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		if ch == '\\' && p.pos+1 < len(p.input) {
			next := p.input[p.pos+1]
			switch next {
			case '"', '\\':
				b.WriteByte(next)
				p.pos += 2
			case 'n':
				b.WriteByte('\n')
				p.pos += 2
			case 't':
				b.WriteByte('\t')
				p.pos += 2
			default:
				b.WriteByte(ch)
				p.pos++
			}
			continue
		}
		if ch == '"' {
			p.pos++ // skip closing "
			return b.String(), nil
		}
		b.WriteByte(ch)
		p.pos++
	}
	return "", fmt.Errorf("unterminated quoted string")
}

func (p *vdfParser) readUnquotedString() string {
	start := p.pos
	for p.pos < len(p.input) && !unicode.IsSpace(rune(p.input[p.pos])) && p.input[p.pos] != '{' && p.input[p.pos] != '}' {
		p.pos++
	}
	return p.input[start:p.pos]
}

func (p *vdfParser) skipWhitespace() {
	for p.pos < len(p.input) && unicode.IsSpace(rune(p.input[p.pos])) {
		p.pos++
	}
}
