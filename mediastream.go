package main

import (
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type MediaStream struct {
	remoteVideoConnection *net.UDPConn
	remoteAudioConnection *net.UDPConn
	remoteVideoTrack      *webrtc.TrackRemote
	remoteAudioTrack      *webrtc.TrackRemote
	remoteVideoMutex      sync.Mutex
	remoteAudioMutex      sync.Mutex
}

func (mediaStream *MediaStream) initializeRemoteStreams() error {
	var localAddress *net.UDPAddr
	var err error

	localAddress, err = net.ResolveUDPAddr("udp", "127.0.0.1:")
	if err != nil {
		panic(fmt.Sprintf("logic: net.ResolveUDPAddr for local - %s", err))
	}

	var remoteAddressAudio *net.UDPAddr
	remoteAddressAudio, err = net.ResolveUDPAddr("udp", "127.0.0.1:4004")
	if err != nil {
		panic(fmt.Sprintf("logic: net.ResolveUDPAddr for remote audio - %s", err))
	}

	mediaStream.remoteAudioConnection, err = net.DialUDP("udp", localAddress, remoteAddressAudio)
	if err != nil {
		return err
	}

	var remoteAddressVideo *net.UDPAddr
	remoteAddressVideo, err = net.ResolveUDPAddr("udp", "127.0.0.1:4006")
	if err != nil {
		panic(fmt.Sprintf("logic: net.ResolveUDPAddr for remote video - %s", err))
	}

	mediaStream.remoteVideoConnection, err = net.DialUDP("udp", localAddress, remoteAddressVideo)
	if err != nil {
		return err
	}

	return nil
}

func (mediaStream *MediaStream) closeRemoteStreams() error {

	if mediaStream.remoteAudioConnection != nil {
		err := mediaStream.remoteAudioConnection.Close()
		mediaStream.remoteAudioConnection = nil

		if err != nil {
			return err
		}
	}

	if mediaStream.remoteVideoConnection != nil {
		err := mediaStream.remoteVideoConnection.Close()
		mediaStream.remoteVideoConnection = nil

		if err != nil {
			return err
		}
	}

	return nil
}

func (mediaStream *MediaStream) sendLocal(room *Room, mediaType MediaType, port int) {
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
		for peerIndex, peer := range room.peers {
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

func (mediaStream *MediaStream) receiveRemote(mediaType MediaType) {
	buf := make([]byte, 1500)
	rtpPacket := &rtp.Packet{}
	for {
		track, connection, payloadType :=
			func(mediaType MediaType) (*webrtc.TrackRemote, *net.UDPConn, uint8) {
				if mediaType == MediaTypeVideo {
					mediaStream.remoteVideoMutex.Lock()
					defer mediaStream.remoteVideoMutex.Unlock()
					return mediaStream.remoteVideoTrack, mediaStream.remoteVideoConnection, 96
				} else {
					mediaStream.remoteAudioMutex.Lock()
					defer mediaStream.remoteAudioMutex.Unlock()
					return mediaStream.remoteAudioTrack, mediaStream.remoteAudioConnection, 111
				}
			}(mediaType)

		if track == nil {
			continue
		}
		if connection == nil {
			break
		}

		n, _, err := track.Read(buf)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"remote stream warning: track read - %s\n",
				err)
			time.Sleep(time.Second)
			continue
		}

		err = rtpPacket.Unmarshal(buf[:n])
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"remote stream warning: rtp packet unmarshal - %s\n",
				err)
			time.Sleep(time.Second)
			continue
		}
		rtpPacket.PayloadType = payloadType

		n, err = rtpPacket.MarshalTo(buf)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"remote stream warning: rtp packet marshal - %s\n",
				err)
			time.Sleep(time.Second)
			continue
		}

		_, err = connection.Write(buf[:n])
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"remote stream warning: rtp packet write - %s\n",
				err)
			time.Sleep(time.Second)
			continue
		}
	}
}
