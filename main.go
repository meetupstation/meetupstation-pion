package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type MediaType int

const (
	MediaTypeAudio MediaType = 0
	MediaTypeVideo MediaType = 1
)

func main() {

	var room Room
	room.nextPeerConnectionId = 0

	if len(os.Args) != 4 ||
		(os.Args[1] != "host" && os.Args[1] != "guest") {
		fmt.Fprintf(os.Stderr,
			"example usage: ./meetupstation-pion [host,guest] https://meetupstation.com \"secret host room id\"\n")
		return
	}

	switch os.Args[1] {
	case "host":
		room.meetingType = MeetingTypeHost
	case "guest":
		room.meetingType = MeetingTypeGuest
	}

	signalServer := os.Args[2]
	room.signalId = os.Args[3]

	// signalServer := "https://meetupstation.com"
	// room.signalId = "secret host room id"
	// room.meetingType = MeetingTypeGuest

	interruptChannel := make(chan os.Signal, 1)
	signal.Notify(interruptChannel, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-interruptChannel
		os.Exit(0)
	}()

	go streamLocalTrack(&room.peers, MediaTypeAudio, 4000)
	go streamLocalTrack(&room.peers, MediaTypeVideo, 4002)

	room.signalAccessKey = ""

	for {
		room.localSessionDescription = ""
		room.localCandidates = []string{}
		room.remoteSessionDescription = ""
		room.remoteCandidates = []string{}
		room.signallingComplete = false

		peerConnectionId := room.nextPeerConnectionId
		fmt.Fprintf(os.Stdout, "conn %d, starting in a second...\n", peerConnectionId)
		time.Sleep(time.Second)

		var err error
		err = room.initializePeerConnection()
		if err != nil {
			fmt.Fprintf(os.Stderr, "conn %d: %s\n", peerConnectionId, err)
			continue
		}
		room.nextPeerConnectionId++

		fmt.Fprintf(os.Stdout, "conn %d: setting up tracks and data handlers\n", peerConnectionId)

		err = room.prepareGuestAnswerOrHostOffer(peerConnectionId, signalServer)
		if err != nil {
			fmt.Fprintf(os.Stderr, "conn %d: %s\n", peerConnectionId, err)
			continue
		}

		fmt.Fprintf(os.Stdout, "conn %d: waiting for ice connection\n", peerConnectionId)

		err = room.waitForIceConnected(peerConnectionId, signalServer)
		if err != nil {
			fmt.Fprintf(os.Stderr, "conn %d: %s\n", peerConnectionId, err)
		} else if room.meetingType == MeetingTypeGuest {
			fmt.Fprintf(os.Stdout, "conn %d: waiting for ice disconnection\n", peerConnectionId)
			err = room.waitForIceDisconnected(peerConnectionId, signalServer)
			if err != nil {
				fmt.Fprintf(os.Stderr, "conn %d: %s\n", peerConnectionId, err)
			}
		}

		// waitForAllICECandidates := webrtc.GatheringCompletePromise(room.getPeer(peerConnectionId).peerConnection)
		// <-waitForAllICECandidates
	}
}
