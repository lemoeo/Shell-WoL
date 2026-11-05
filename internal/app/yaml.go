package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Reject aliases and duplicate keys; retain comments on existing mapping values.
func parseYAML(b []byte) (*yaml.Node, error) {
	var n yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&n); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("只允许一个 YAML 文档")
	}
	var check func(*yaml.Node) error
	check = func(p *yaml.Node) error {
		if p.Kind == yaml.AliasNode || p.Anchor != "" {
			return errors.New("不支持 YAML 锚点和别名")
		}
		if p.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(p.Content); i += 2 {
				k := p.Content[i]
				if k.Kind != yaml.ScalarNode || k.Tag != "!!str" {
					return errors.New("YAML 字段名必须是字符串")
				}
				if seen[k.Value] {
					return fmt.Errorf("重复 YAML 字段: %s", k.Value)
				}
				seen[k.Value] = true
			}
		}
		for _, c := range p.Content {
			if err := check(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := check(&n); err != nil {
		return nil, err
	}
	return &n, nil
}
func readYAML(path string, out any) (*yaml.Node, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	if len(b) > 2<<20 {
		return nil, errors.New("YAML 文件超过 2 MiB")
	}
	n, e := parseYAML(b)
	if e != nil {
		return nil, e
	}
	if e = n.Decode(out); e != nil {
		return nil, e
	}
	return n, nil
}
func root(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0]
	}
	return n
}
func field(n *yaml.Node, key string) *yaml.Node {
	n = root(n)
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
func copyComments(dst, src *yaml.Node) {
	dst.HeadComment = src.HeadComment
	dst.LineComment = src.LineComment
	dst.FootComment = src.FootComment
}
func mergeNode(old, next *yaml.Node) *yaml.Node {
	if old == nil {
		return next
	}
	copyComments(next, old)
	if old.Kind == yaml.MappingNode && next.Kind == yaml.MappingNode {
		for i := 0; i < len(next.Content); i += 2 {
			key := next.Content[i]
			value := next.Content[i+1]
			found := false
			for j := 0; j < len(old.Content); j += 2 {
				if old.Content[j].Value == key.Value {
					old.Content[j+1] = mergeNode(old.Content[j+1], value)
					found = true
					break
				}
			}
			if !found {
				old.Content = append(old.Content, key, value)
			}
		}
		return old
	}
	if old.Kind == yaml.SequenceNode && next.Kind == yaml.SequenceNode {
		for i, n := range next.Content {
			key := field(n, "id")
			if key == nil {
				continue
			}
			for _, o := range old.Content {
				prior := field(o, "id")
				if prior != nil && prior.Value == key.Value {
					next.Content[i] = mergeNode(o, n)
					break
				}
			}
		}
	}
	return next
}
func encodeYAML(old *yaml.Node, value any) ([]byte, error) {
	var next yaml.Node
	if e := next.Encode(value); e != nil {
		return nil, e
	}
	if old != nil {
		next = *mergeNode(root(old), &next)
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if e := enc.Encode(&next); e != nil {
		return nil, e
	}
	if e := enc.Close(); e != nil {
		return nil, e
	}
	return b.Bytes(), nil
}
func atomicWrite(path string, b []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".wol-*.tmp")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(0600); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}
func saveYAML(path string, old *yaml.Node, value any) (*yaml.Node, error) {
	// Clone before merging so a failed write cannot mutate the in-memory document.
	var clone *yaml.Node
	if old != nil {
		b, e := yaml.Marshal(old)
		if e != nil {
			return nil, e
		}
		clone, e = parseYAML(b)
		if e != nil {
			return nil, e
		}
	}
	b, e := encodeYAML(clone, value)
	if e != nil {
		return nil, e
	}
	if len(b) > 2<<20 {
		return nil, errors.New("保存后的 YAML 超过 2 MiB，请减少设备或备注数量")
	}
	if e = atomicWrite(path, b); e != nil {
		return nil, e
	}
	return parseYAML(b)
}
