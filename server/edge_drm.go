package main

// 微软朗读服务的 Sec-MS-GEC 令牌（v2）。
// 2026 年起服务端改用 v2：令牌是 “<CS64 16 位十六进制>-<SHA256 64 位十六进制>”，
// 其中 SHA256 前面拼了一段固定的 57 字节前缀（这里用算到第 14 轮的中间状态表示），
// CS64 是一个自定义的 64 位校验和。算法照搬 edge-tts（rany2/edge-tts，src/edge_tts/drm.py），
// 测试向量也是用它生成的，见 edge_drm_test.go。

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/bits"
	"strings"
)

const winEpoch = 11644473600

var gecSHAInitial = [8]uint32{0x6A09E667, 0xBB67AE85, 0x3C6EF372, 0xA54FF53A, 0x510E527F, 0x9B05688C, 0x1F83D9AB, 0x5BE0CD19}

// 前缀算完 14 轮后的工作寄存器
var gecSHAState = [8]uint32{0xFD9FBEA2, 0x91243CE7, 0xBE499094, 0xCD2E1E0A, 0x6C006F49, 0x04BD35AB, 0xFEB0A7E4, 0x03EFFBD2}

// 前缀对应的第 16～29 个扩展消息字的常量部分
var gecSHASchedule = [14]uint32{0xEB24BF9E, 0x1558691F, 0xA0C09EE2, 0x0CFA32BA, 0xA622EE8C, 0xD1DD9491, 0xC0DD25A0, 0xF9E70AB6, 0xF0C31573, 0x7058CE4E, 0x7188E606, 0x00E5AB33, 0xD258B060, 0x72766564}

var gecK = [64]uint32{
	0x428A2F98, 0x71374491, 0xB5C0FBCF, 0xE9B5DBA5, 0x3956C25B, 0x59F111F1, 0x923F82A4, 0xAB1C5ED5,
	0xD807AA98, 0x12835B01, 0x243185BE, 0x550C7DC3, 0x72BE5D74, 0x80DEB1FE, 0x9BDC06A7, 0xC19BF174,
	0xE49B69C1, 0xEFBE4786, 0x0FC19DC6, 0x240CA1CC, 0x2DE92C6F, 0x4A7484AA, 0x5CB0A9DC, 0x76F988DA,
	0x983E5152, 0xA831C66D, 0xB00327C8, 0xBF597FC7, 0xC6E00BF3, 0xD5A79147, 0x06CA6351, 0x14292967,
	0x27B70A85, 0x2E1B2138, 0x4D2C6DFC, 0x53380D13, 0x650A7354, 0x766A0ABB, 0x81C2C92E, 0x92722C85,
	0xA2BFE8A1, 0xA81A664B, 0xC24B8B70, 0xC76C51A3, 0xD192E819, 0xD6990624, 0xF40E3585, 0x106AA070,
	0x19A4C116, 0x1E376C08, 0x2748774C, 0x34B0BCB5, 0x391C0CB3, 0x4ED8AA4A, 0x5B9CCA4F, 0x682E6FF3,
	0x748F82EE, 0x78A5636F, 0x84C87814, 0x8CC70208, 0x90BEFFFA, 0xA4506CEB, 0xBEF9A3F7, 0xC67178F2,
}

const (
	gecSHAAdjustment = 0xC9DC06A7
	gecSigma0Adj     = 0x059C0B80
	gecSigma1Adj     = 0x000B92C0
	gecCS64Factor    = 0x82893933
	gecCS64Adj0      = 0xF2F90B1A
	gecCS64Adj1      = 0x0B1A0000
)

var gecCS64Coefficients = [14][2]uint32{
	{0xE82E91B1, 0xB32700D0}, {0x44DE24C6, 0x88DFF51D}, {0x3BEB65BC, 0x29514038}, {0xC89C5933, 0xB328CBDF},
	{0x8D1EC211, 0xD405B4D5}, {0x314EF5D5, 0x26E55F3C}, {0xE92491B1, 0x607801C6}, {0xF0A5B9D5, 0x82B2C1FB},
	{0x06064CF3, 0xFA51A3DA}, {0xDA79FA60, 0x207CC464}, {0xBED20560, 0x3C3C74F3}, {0x89F9C9F8, 0xD2D14B61},
	{0x35FA38C6, 0xBB04A2C1}, {0xCB49E3C6, 0xC5626167},
}

func gecSigma0(x uint32) uint32 {
	return bits.RotateLeft32(x, -7) ^ bits.RotateLeft32(x, -18) ^ (x >> 3)
}
func gecSigma1(x uint32) uint32 {
	return bits.RotateLeft32(x, -17) ^ bits.RotateLeft32(x, -19) ^ (x >> 10)
}
func rol16(x uint32) uint32 { return bits.RotateLeft32(x, 16) }

// 标准 SHA-256 压缩轮，从第 first 轮开始；从第 14 轮开始时第 14 个常数换成调整值
func gecRounds(state [8]uint32, w []uint32, first int) [8]uint32 {
	k := gecK
	if first == 14 {
		k[14] = gecSHAAdjustment
	}
	a, b, c, d, e, f, g, h := state[0], state[1], state[2], state[3], state[4], state[5], state[6], state[7]
	for i := first; i < 64; i++ {
		s1 := bits.RotateLeft32(e, -6) ^ bits.RotateLeft32(e, -11) ^ bits.RotateLeft32(e, -25)
		ch := g ^ (e & (f ^ g))
		t1 := h + s1 + ch + k[i] + w[i]
		s0 := bits.RotateLeft32(a, -2) ^ bits.RotateLeft32(a, -13) ^ bits.RotateLeft32(a, -22)
		maj := (a & b) | (c & (a | b))
		t2 := s0 + maj
		h, g, f, e, d, c, b, a = g, f, e, d+t1, c, b, a, t1+t2
	}
	return [8]uint32{a, b, c, d, e, f, g, h}
}

func gecExpand(w []uint32, from int) []uint32 {
	for i := from; i < 64; i++ {
		w = append(w, gecSigma1(w[i-2])+w[i-7]+gecSigma0(w[i-15])+w[i-16])
	}
	return w
}

// 前缀 + 数据前 7 个字节组成第一个块
func gecInit(d7 []byte) [8]uint32 {
	w := make([]uint32, 14, 64)
	w = append(w, uint32(d7[0])<<16|uint32(d7[1])<<8|uint32(d7[2]))
	w = append(w, binary.BigEndian.Uint32(d7[3:7]))
	for i := 16; i < 30; i++ {
		v := gecSHASchedule[i-16]
		if i == 16 {
			v += gecSigma1(w[i-2]) ^ gecSigma1Adj
		} else {
			v += gecSigma1(w[i-2])
		}
		if i-7 >= 14 {
			v += w[i-7]
		}
		if i == 29 {
			v += gecSigma0(w[i-15]) ^ gecSigma0Adj
		}
		w = append(w, v)
	}
	w = append(w, gecSigma1(w[28])+w[23]+gecSigma0(w[15])+w[14]+gecSHAAdjustment-gecK[14])
	w = gecExpand(w, 31)
	work := gecRounds(gecSHAState, w, 14)
	var out [8]uint32
	for i := range out {
		out[i] = gecSHAInitial[i] + work[i]
	}
	return out
}

func gecSHA256(data []byte) ([]byte, error) {
	if len(data) < 7 {
		return nil, fmt.Errorf("令牌数据太短")
	}
	length := 57 + len(data)
	pad := ((55-length)%64 + 64) % 64
	msg := append(append([]byte{}, data[7:]...), 0x80)
	msg = append(msg, make([]byte, pad)...)
	msg = binary.BigEndian.AppendUint64(msg, uint64(length)*8)
	state := gecInit(data[:7])
	for off := 0; off+64 <= len(msg); off += 64 {
		w := make([]uint32, 16, 64)
		for i := range 16 {
			w[i] = binary.BigEndian.Uint32(msg[off+i*4:])
		}
		w = gecExpand(w, 16)
		work := gecRounds(state, w, 0)
		for i := range state {
			state[i] += work[i]
		}
	}
	out := make([]byte, 32)
	for i, v := range state {
		binary.BigEndian.PutUint32(out[i*4:], v)
	}
	return out, nil
}

func gecCS64(data, digest []byte) (uint32, uint32) {
	keyA := binary.LittleEndian.Uint32(digest[0:4]) | 1
	keyB := binary.LittleEndian.Uint32(digest[4:8]) | 1
	factors := [2]uint32{keyA * gecCS64Factor, keyB * gecCS64Factor}
	var terms, offsets []uint32
	for i, c := range gecCS64Coefficients {
		terms = append(terms, c[0]*factors[i%2])
		offsets = append(offsets, c[1])
	}
	// 前面补一个 0 字节，按小端 4 字节一组，凑成偶数个字
	buf := append([]byte{0}, data...)
	for len(buf)%4 != 0 {
		buf = append(buf, 0)
	}
	var words []uint32
	for i := 0; i < len(buf); i += 4 {
		words = append(words, binary.LittleEndian.Uint32(buf[i:]))
	}
	if len(words)%2 == 1 {
		words = append(words, 0)
	}
	for i, wd := range words {
		var term, off uint32
		if i%2 == 0 {
			term, off = wd*keyA, rol16(wd)*0xEF0569FB
		} else {
			term, off = wd*keyB, rol16(wd)*0xC31713DB
		}
		if i == 0 {
			term += gecCS64Adj0 * factors[0]
			off += gecCS64Adj1
		}
		terms = append(terms, term)
		offsets = append(offsets, off)
	}
	var swapAcc, swapLast, state, acc uint32
	for i := 0; i+1 < len(terms); i += 2 {
		x, y := terms[i], terms[i+1]
		t := rol16(offsets[i] + x)
		q := t * 0x76F50F34
		swapAcc += rol16(q)*0xC3EFEA97 + q
		t = rol16(offsets[i+1] + y)
		q = t * 0x1881CE3C
		swapLast = rol16(q)*0x35BD1EC9 + q
		swapAcc += swapLast

		state = state*keyA + x
		state = rol16(state) * 0xCF98B111
		state = rol16(state) * 0x87085B9F
		state = rol16(state) * 0x12CEB96D
		state = rol16(state) * 0x257E1D83
		acc += state
		t = state*keyB + y
		t = rol16(t) * 0xA27416F5
		t = rol16(t) * 0xD38396FF
		state = rol16(rol16(t)*0x7C932B89) * 0xBFA49F69
		acc += state
	}
	return swapLast ^ state, swapAcc ^ acc
}

func gecTokenHash(data []byte) (string, string, error) {
	digest, err := gecSHA256(data)
	if err != nil {
		return "", "", err
	}
	a, b := gecCS64(data, digest)
	cs := make([]byte, 8)
	binary.LittleEndian.PutUint32(cs, a)
	binary.LittleEndian.PutUint32(cs[4:], b)
	return strings.ToUpper(hex.EncodeToString(cs)), strings.ToUpper(hex.EncodeToString(digest)), nil
}

// unix 是（校正过时钟偏差的）当前 Unix 秒数
func edgeGECv2(unix int64) string {
	t := unix + winEpoch
	t -= t % 300
	ticks := uint64(t) * 10_000_000
	cs, sha, err := gecTokenHash([]byte(fmt.Sprintf("%d%s", ticks, edgeToken)))
	if err != nil {
		return ""
	}
	return cs + "-" + sha
}
