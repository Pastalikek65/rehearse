package forgejo

import (
	"context"
	"io"
)

type contextReader struct {
	ctx context.Context
	r   io.Reader
	err error
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		r.err = err
		return 0, err
	}
	n, err := r.r.Read(p)
	if err != nil && err != io.EOF && r.ctx.Err() != nil {
		r.err = r.ctx.Err()
		return n, r.err
	}
	return n, err
}

type countingReader struct {
	r     io.Reader
	bytes uint64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.bytes += uint64(n)
	return n, err
}

type contextReadCloser struct {
	r  io.Reader
	cl io.Closer
}

func (r *contextReadCloser) Read(p []byte) (int, error) { return r.r.Read(p) }
func (r *contextReadCloser) Close() error               { return r.cl.Close() }

func archiveReadError(cr *contextReader, err error) error {
	if err == nil {
		return nil
	}
	if cr != nil && cr.ctx.Err() != nil {
		return ErrArchiveCanceled
	}
	return ErrArchiveInvalid
}
