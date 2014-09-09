package index

import (
	"bytes"
	"encoding"
	"encoding/binary"
	"io"
	"sync"

	clientmodel "github.com/prometheus/client_golang/model"

	"github.com/prometheus/prometheus/storage/metric"
)

type codable interface {
	encoding.BinaryMarshaler
	encoding.BinaryUnmarshaler
}

// TODO: yeah, this ain't ideal. A lot of locking and possibly even contention.
var tmpBufMtx sync.Mutex
var tmpBuf = make([]byte, binary.MaxVarintLen64)

func setTmpBufLen(l int) {
	if cap(tmpBuf) >= l {
		tmpBuf = tmpBuf[:l]
	} else {
		tmpBuf = make([]byte, l)
	}
}

func encodeVarint(b *bytes.Buffer, i int) error {
	tmpBufMtx.Lock()
	defer tmpBufMtx.Unlock()

	bytesWritten := binary.PutVarint(tmpBuf, int64(i))
	if _, err := b.Write(tmpBuf[:bytesWritten]); err != nil {
		return err
	}
	return nil
}

func encodeString(b *bytes.Buffer, s string) error {
	encodeVarint(b, len(s))
	if _, err := b.WriteString(s); err != nil {
		return err
	}
	return nil
}

func decodeString(b *bytes.Reader) (string, error) {
	length, err := binary.ReadVarint(b)
	if err != nil {
		return "", err
	}

	tmpBufMtx.Lock()
	defer tmpBufMtx.Unlock()

	setTmpBufLen(int(length))
	if _, err := io.ReadFull(b, tmpBuf); err != nil {
		return "", err
	}
	return string(tmpBuf), nil
}

type codableMetric clientmodel.Metric

func (m codableMetric) MarshalBinary() ([]byte, error) {
	buf := &bytes.Buffer{}
	encodeVarint(buf, len(m))
	for l, v := range m {
		encodeString(buf, string(l))
		encodeString(buf, string(v))
	}
	return buf.Bytes(), nil
}

func (m codableMetric) UnmarshalBinary(buf []byte) error {
	r := bytes.NewReader(buf)
	numLabelPairs, err := binary.ReadVarint(r)
	if err != nil {
		return err
	}
	for ; numLabelPairs > 0; numLabelPairs-- {
		ln, err := decodeString(r)
		if err != nil {
			return err
		}
		lv, err := decodeString(r)
		if err != nil {
			return err
		}
		m[clientmodel.LabelName(ln)] = clientmodel.LabelValue(lv)
	}
	return nil
}

type codableFingerprint clientmodel.Fingerprint

func (fp codableFingerprint) MarshalBinary() ([]byte, error) {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(fp))
	return b, nil
}

func (fp *codableFingerprint) UnmarshalBinary(buf []byte) error {
	*fp = codableFingerprint(binary.BigEndian.Uint64(buf))
	return nil
}

type codableFingerprints clientmodel.Fingerprints

func (fps codableFingerprints) MarshalBinary() ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, binary.MaxVarintLen64+len(fps)*8))
	encodeVarint(buf, len(fps))

	tmpBufMtx.Lock()
	defer tmpBufMtx.Unlock()

	setTmpBufLen(8)
	for _, fp := range fps {
		binary.BigEndian.PutUint64(tmpBuf, uint64(fp))
		if _, err := buf.Write(tmpBuf[:8]); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func (fps *codableFingerprints) UnmarshalBinary(buf []byte) error {
	r := bytes.NewReader(buf)
	numFPs, err := binary.ReadVarint(r)
	if err != nil {
		return err
	}
	*fps = make(codableFingerprints, numFPs)

	offset := len(buf) - r.Len()
	for i, _ := range *fps {
		(*fps)[i] = clientmodel.Fingerprint(binary.BigEndian.Uint64(buf[offset+i*8:]))
	}
	return nil
}

type codableLabelPair metric.LabelPair

func (lp codableLabelPair) MarshalBinary() ([]byte, error) {
	buf := &bytes.Buffer{}
	encodeString(buf, string(lp.Name))
	encodeString(buf, string(lp.Value))
	return buf.Bytes(), nil
}

func (lp *codableLabelPair) UnmarshalBinary(buf []byte) error {
	r := bytes.NewReader(buf)
	n, err := decodeString(r)
	if err != nil {
		return err
	}
	v, err := decodeString(r)
	if err != nil {
		return err
	}
	lp.Name = clientmodel.LabelName(n)
	lp.Value = clientmodel.LabelValue(v)
	return nil
}

type codableLabelName clientmodel.LabelName

func (l codableLabelName) MarshalBinary() ([]byte, error) {
	buf := &bytes.Buffer{}
	encodeString(buf, string(l))
	return buf.Bytes(), nil
}

func (l *codableLabelName) UnmarshalBinary(buf []byte) error {
	r := bytes.NewReader(buf)
	n, err := decodeString(r)
	if err != nil {
		return err
	}
	*l = codableLabelName(n)
	return nil
}

type codableLabelValues clientmodel.LabelValues

func (vs codableLabelValues) MarshalBinary() ([]byte, error) {
	buf := &bytes.Buffer{}
	encodeVarint(buf, len(vs))
	for _, v := range vs {
		encodeString(buf, string(v))
	}
	return buf.Bytes(), nil
}

func (vs *codableLabelValues) UnmarshalBinary(buf []byte) error {
	r := bytes.NewReader(buf)
	numValues, err := binary.ReadVarint(r)
	if err != nil {
		return err
	}
	*vs = make(codableLabelValues, numValues)

	for i, _ := range *vs {
		v, err := decodeString(r)
		if err != nil {
			return err
		}
		(*vs)[i] = clientmodel.LabelValue(v)
	}
	return nil
}

type codableMembership struct{}

func (m codableMembership) MarshalBinary() ([]byte, error) {
	return []byte{}, nil
}

func (m codableMembership) UnmarshalBinary(buf []byte) error { return nil }
