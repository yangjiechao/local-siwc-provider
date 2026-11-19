package main

// 缩短大图片字段后再解析用量，避免把图像内容完整缓存在内存中。
type usageJSONCapture struct {
	buf         []byte
	tooLarge    bool
	inString    bool
	escaped     bool
	keyPossible bool
	key         []byte
	afterKey    int
	inImage     bool
}

func (c *usageJSONCapture) appendByte(b byte) {
	if c.tooLarge {
		return
	}
	if len(c.buf) >= 1<<20 {
		c.buf = nil
		c.tooLarge = true
		return
	}
	c.buf = append(c.buf, b)
}

func (c *usageJSONCapture) write(data []byte) {
	for _, ch := range data {
		if c.tooLarge {
			return
		}
		if c.inImage {
			if ch == '"' {
				c.appendByte(ch)
				c.inImage = false
				continue
			}
			if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '+' || ch == '/' || ch == '=' || ch == '-' || ch == '_' {
				continue
			}
			c.buf = nil
			c.tooLarge = true
			return
		}
		if c.inString {
			c.appendByte(ch)
			if c.escaped {
				c.escaped = false
				c.keyPossible = false
				continue
			}
			if ch == '\\' {
				c.escaped = true
				c.keyPossible = false
				continue
			}
			if ch == '"' {
				c.inString = false
				if c.keyPossible && string(c.key) == "b64_json" {
					c.afterKey = 1
				}
				continue
			}
			if c.keyPossible {
				if len(c.key) < 8 {
					c.key = append(c.key, ch)
				} else {
					c.keyPossible = false
				}
			}
			continue
		}
		if c.afterKey > 0 {
			if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
				c.appendByte(ch)
				continue
			}
			if c.afterKey == 1 && ch == ':' {
				c.appendByte(ch)
				c.afterKey = 2
				continue
			}
			if c.afterKey == 2 && ch == '"' {
				c.appendByte(ch)
				c.afterKey = 0
				c.inImage = true
				continue
			}
			c.afterKey = 0
		}
		c.appendByte(ch)
		if ch == '"' {
			c.inString = true
			c.escaped = false
			c.keyPossible = true
			c.key = c.key[:0]
		}
	}
}
