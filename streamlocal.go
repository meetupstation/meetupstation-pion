package main

import (
	"fmt"
	"net"
	"os"

	"github.com/pion/webrtc/v4"
)

func streamLocalTrack(peers *[]*Peer, mediaType MediaType, port int) {
	listener, err := net.ListenUDP(
		"udp",
		&net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: port,
		})

	if err != nil {
		fmt.Fprintf(os.Stderr,
			"net.ListenUDP, %s\n",
			err)
	}

	defer func() {
		if err = listener.Close(); err != nil {
			fmt.Fprintf(os.Stderr,
				"listener.Close, %s\n",
				err)
		}
	}()

	// Increase the UDP receive buffer size
	// Default UDP buffer sizes vary on different operating systems
	bufferSize := 300000 // 300KB
	err = listener.SetReadBuffer(bufferSize)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"listener.SetReadBuffer, %s\n",
			err)
	}

	inboundRTPPacket := make([]byte, 1600) // UDP MTU
	for {
		readBytes, _, err := listener.ReadFrom(inboundRTPPacket)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"listener.ReadFrom: %s\n",
				err)
		}

		// fmt.Println(readBytes)
		for peerIndex, peer := range *peers {
			track := func() *webrtc.TrackLocalStaticRTP {
				if mediaType == MediaTypeVideo {
					return peer.localVideoTrack
				} else {
					return peer.localAudioTrack
				}
			}()

			if peer.IsNull() {
				continue
			}

			_, err = track.Write(inboundRTPPacket[:readBytes])
			if err != nil {
				// if errors.Is(err, io.ErrClosedPipe) {
				// 	peer.Close(peerIndex)
				// }

				fmt.Fprintf(os.Stderr,
					"conn %d: while write to track: %s\n",
					peerIndex,
					err)
			}
		}
		// fmt.Println(writtenBytes)
	}
}
