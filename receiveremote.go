package main

import (
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func (peer *Peer) receiveRemote(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	connection, payloadType :=
		func(track *webrtc.TrackRemote) (*net.UDPConn, uint8) {
			if track.Kind().String() == "video" {
				return peer.remoteVideoConnection, 96
			} else {
				return peer.remoteAudioConnection, 111
			}
		}(track)

	buf := make([]byte, 1500)
	rtpPacket := &rtp.Packet{}
	for {
		if peer.IsNull() {
			break
		}

		n, _, err := track.Read(buf)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: track read - %s\n",
				peer.peerConnectionId,
				err)
			break
		}

		err = rtpPacket.Unmarshal(buf[:n])
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: rtp packet unmarshal - %s\n",
				peer.peerConnectionId,
				err)
		}
		rtpPacket.PayloadType = payloadType

		n, err = rtpPacket.MarshalTo(buf)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: rtp packet marshal - %s\n",
				peer.peerConnectionId,
				err)
		}

		_, err = connection.Write(buf[:n])
		if err != nil {
			var opError *net.OpError
			if errors.As(err, &opError) &&
				opError.Err.Error() == "write: connection refused" {
				continue
			}

			fmt.Fprintf(os.Stderr,
				"conn %d: rtp packet write - %s\n",
				peer.peerConnectionId,
				err)

			break
		}
	}
}
