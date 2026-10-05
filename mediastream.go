package main

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
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
	localVideoListener    *net.UDPConn
	localAudioListener    *net.UDPConn
}

func (mediaStream *MediaStream) initializeRemoteStreams(audioPort int, videoPort int) error {
	var err error
	mediaStream.remoteAudioConnection, err = net.DialUDP(
		"udp",
		&net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: 0,
		},
		&net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: audioPort,
		})
	if err != nil {
		return err
	}

	mediaStream.remoteVideoConnection, err = net.DialUDP(
		"udp",
		&net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: 0,
		},
		&net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: videoPort,
		})
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

func (mediaStream *MediaStream) initializeLocalStreams(audioPort int, videoPort int) error {
	var err error
	mediaStream.localAudioListener, err = net.ListenUDP(
		"udp",
		&net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: audioPort,
		})
	if err != nil {
		return err
	}

	mediaStream.localVideoListener, err = net.ListenUDP(
		"udp",
		&net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: videoPort,
		})
	if err != nil {
		return err
	}

	// Increase the UDP receive buffer size
	// Default UDP buffer sizes vary on different operating systems
	bufferSize := 300000 // 300KB
	err = mediaStream.localAudioListener.SetReadBuffer(bufferSize)
	if err != nil {
		return err
	}
	err = mediaStream.localVideoListener.SetReadBuffer(bufferSize)
	if err != nil {
		return err
	}

	return nil
}

func (mediaStream *MediaStream) closeLocalStreams() error {

	if mediaStream.localAudioListener != nil {
		err := mediaStream.localAudioListener.Close()
		mediaStream.localAudioListener = nil

		if err != nil {
			return err
		}
	}

	if mediaStream.localVideoListener != nil {
		err := mediaStream.localVideoListener.Close()
		mediaStream.localVideoListener = nil

		if err != nil {
			return err
		}
	}

	return nil
}

func (mediaStream *MediaStream) sendLocal(room *Room,
	mediaType MediaType,
	running *atomic.Bool) {

	listener := func(mediaType MediaType) *net.UDPConn {
		if mediaType == MediaTypeAudio {
			return mediaStream.localAudioListener
		} else {
			return mediaStream.localVideoListener
		}
	}(mediaType)

	inboundRTPPacket := make([]byte, 1600) // UDP MTU
	for running.Load() {
		listener.SetReadDeadline(time.Now().Add(time.Second))

		readBytes, _, err := listener.ReadFrom(inboundRTPPacket)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			if errors.Is(err, net.ErrClosed) {
				fmt.Fprintf(os.Stderr, "listener.ReadFrom: %s\n", err)
				return
			}
			fmt.Fprintf(os.Stderr, "listener.ReadFrom: %s\n", err)
			time.Sleep(time.Second)
			continue
		}

		// fmt.Println(readBytes)
		room.peersMutex.RLock()
		peers := make(map[uint64]*Peer, len(room.peers))
		maps.Copy(peers, room.peers)
		room.peersMutex.RUnlock()

		for peerConnectionId, peer := range peers {
			track := func() *webrtc.TrackLocalStaticRTP {
				if mediaType == MediaTypeVideo {
					return peer.localVideoTrack
				} else {
					return peer.localAudioTrack
				}
			}()

			peerConnection := peer.getConnection()
			if peerConnection == nil {
				continue
			}

			state := peerConnection.ICEConnectionState()

			if state != webrtc.ICEConnectionStateConnected &&
				state != webrtc.ICEConnectionStateCompleted {
				continue
			}

			// var writtenBytes int
			_, err = track.Write(inboundRTPPacket[:readBytes])
			if err != nil {
				fmt.Fprintf(os.Stderr,
					"conn %d: while write to track: %s\n",
					peerConnectionId,
					err)
			}

			// fmt.Println(writtenBytes)
		}
	}
}

func (mediaStream *MediaStream) receiveRemote(mediaType MediaType,
	running *atomic.Bool) {

	buf := make([]byte, 1500)
	rtpPacket := &rtp.Packet{}
	for running.Load() {
		getStringMediaType := func(mediaType MediaType) string {
			if mediaType == MediaTypeVideo {
				return "video"
			} else {
				return "audio"
			}
		}
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
			// fmt.Fprintf(os.Stderr,
			// 	"remote stream warning: nil remote track - %s\n",
			// 	getStringMediaType(mediaType))
			time.Sleep(time.Second)
			continue
		}
		if connection == nil {
			fmt.Fprintf(os.Stderr,
				"remote stream error: nil remote connection - %s\n",
				getStringMediaType(mediaType))
			return
		}

		track.SetReadDeadline(time.Now().Add(time.Second))

		n, _, err := track.Read(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				// fmt.Fprintf(os.Stderr,
				// 	"remote stream warning: timeout %s - %s\n",
				// 	getStringMediaType(mediaType),
				// 	err)
				continue
			}
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
			continue
		}

		rtpPacket.PayloadType = payloadType

		n, err = rtpPacket.MarshalTo(buf)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"remote stream warning: rtp packet marshal - %s\n",
				err)
			continue
		}

		_, err = connection.Write(buf[:n])
		if err != nil {
			if errors.Is(err, syscall.ECONNREFUSED) {
				continue
			}
			fmt.Fprintf(os.Stderr,
				"remote stream warning: rtp packet write - %s\n",
				err)
			continue
		}
	}
}
