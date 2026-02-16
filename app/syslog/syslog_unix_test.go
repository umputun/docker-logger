//go:build !windows && !nacl && !plan9

package syslog

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsSupported(t *testing.T) {
	assert.True(t, IsSupported())
}

func TestGetWriter(t *testing.T) {
	// start a udp listener to accept syslog messages
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer conn.Close()

	w, err := GetWriter(conn.LocalAddr().String(), "docker/", "container1")
	require.NoError(t, err)
	require.NotNil(t, w)

	_, err = w.Write([]byte("test syslog message"))
	require.NoError(t, err)

	// read from udp listener to verify message was sent
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	buf := make([]byte, 1024)
	n, _, err := conn.ReadFrom(buf)
	require.NoError(t, err)
	assert.Contains(t, string(buf[:n]), "test syslog message")
	assert.Contains(t, string(buf[:n]), "docker/container1")

	assert.NoError(t, w.Close())
}

func TestGetWriter_InvalidHost(t *testing.T) {
	// syslog.Dial with udp doesn't fail on invalid host since udp is connectionless,
	// but an empty protocol will fail
	w, err := GetWriter("", "docker/", "container1")
	if err != nil {
		assert.Nil(t, w)
		return
	}
	// if no error, writer should still be valid
	assert.NotNil(t, w)
	assert.NoError(t, w.Close())
}
