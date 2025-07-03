package main

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/pion/webrtc/v4"
)

type PeerType int

const (
	PeerTypeHost  PeerType = 0
	PeerTypeGuest PeerType = 1
)

type MediaType int

const (
	MediaTypeAudio MediaType = 0
	MediaTypeVideo MediaType = 1
)

func main() {
	if len(os.Args) != 4 ||
		(os.Args[1] != "host" && os.Args[1] != "guest") {
		fmt.Fprintf(os.Stderr,
			"example usage: ./host-golang [host,guest] https://meetupstation.com \"secret host room id\"\n")
		return
	}

	var peerType PeerType

	switch os.Args[1] {
	case "host":
		peerType = PeerTypeHost
	case "guest":
		peerType = PeerTypeGuest
	}

	signalServer := os.Args[2]
	hostId := os.Args[3]
	// signalServer := "https://meetupstation.com"
	// hostId := "secret host room id"
	// peerType := PeerTypeHost

	var peers []Peer

	interruptChannel := make(chan os.Signal, 1)
	signal.Notify(interruptChannel, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-interruptChannel
		os.Exit(0)
	}()

	var mutex sync.Mutex

	go streamLocalTrack(&peers, MediaTypeAudio, 4000)
	go streamLocalTrack(&peers, MediaTypeVideo, 4002)

	for {
		var err error
		peerIndex, connectedChannel := newPeerConnection(&peers, &mutex)

		var localSessionDescription webrtc.SessionDescription

		if peerType == PeerTypeHost {
			for {
				mutex.Lock()
				offerSessionDescription, err := peers[peerIndex].peerConnection.CreateOffer(nil)
				mutex.Unlock()

				if err != nil {
					fmt.Fprintf(os.Stderr, "while creating offer: %s\n", err)
					continue
				}
				localSessionDescription = offerSessionDescription
				break
			}
		} else {
			hostOffer := signalWaitForHost(signalServer, hostId, peerIndex)

			for {
				mutex.Lock()
				err = peers[peerIndex].peerConnection.SetRemoteDescription(hostOffer)
				mutex.Unlock()

				if err != nil {
					fmt.Fprintf(os.Stderr,
						"while setting remote description: %s\n",
						err)
					continue
				}
				break
			}

			for {
				mutex.Lock()
				answerSessionDescription, err := peers[peerIndex].peerConnection.CreateAnswer(nil)
				mutex.Unlock()

				if err != nil {
					fmt.Fprintf(os.Stderr,
						"while creating answer: %s\n",
						err)
					continue
				}
				localSessionDescription = answerSessionDescription
				break
			}
		}

		// later will be locking untill this channel completes
		mutex.Lock()
		waitForAllICECandidates := webrtc.GatheringCompletePromise(peers[peerIndex].peerConnection)
		mutex.Unlock()

		for {
			mutex.Lock()
			err = peers[peerIndex].peerConnection.SetLocalDescription(localSessionDescription)
			mutex.Unlock()

			if err != nil {
				fmt.Fprintf(os.Stderr,
					"while setting local description: %s\n",
					err)
				continue
			}
			break
		}

		fmt.Fprintf(os.Stderr, "conn %d: waiting for all ice candidates\n", peerIndex)
		<-waitForAllICECandidates

		fmt.Fprintf(os.Stderr, "conn %d: all ice candidates are received from stun server\n", peerIndex)

		if peerType == PeerTypeHost {
			// var peerLocalSessionDescription *webrtc.SessionDescription
			// mutex.Lock()
			// peerLocalSessionDescription = peers[peerIndex].peerConnection.LocalDescription()
			// mutex.Unlock()

			fmt.Fprintf(os.Stderr, "conn %d: waiting for the peer to join\n", peerIndex)

			guestAnswer := signalWaitForGuest(signalServer,
				hostId,
				peerIndex,
				localSessionDescription)

			setupTracksAndDataHandlers(&peers, peerIndex)

			peers[peerIndex].peerConnection.SetRemoteDescription(guestAnswer)
		} else {
			// var peerLocalSessionDescription *webrtc.SessionDescription
			// mutex.Lock()
			// peerLocalSessionDescription = peers[peerIndex].peerConnection.LocalDescription()
			// mutex.Unlock()
			signalGuestSetup(signalServer,
				hostId,
				localSessionDescription,
				peerIndex)

			setupTracksAndDataHandlers(&peers, peerIndex)
		}

		fmt.Fprintf(os.Stderr, "conn %d: joined: waiting for the ice connection\n", peerIndex)
		connected := <-connectedChannel

		if connected {
			fmt.Fprintf(os.Stderr, "conn %d: ice connected\n", peerIndex)
			if peerType == PeerTypeGuest {
				connected = <-connectedChannel
			}
		}

		if !connected {
			fmt.Fprintf(os.Stderr, "conn %d: ice disconnected\n", peerIndex)
		}
	}
}
