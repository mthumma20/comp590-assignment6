package main

import (
	"fmt"
	"math"
	"math/rand"
	"time"
)

type Config struct {
	TotalCustomers             int
	WaitingRoomCapacity        int
	ArrivalMinMs               int
	ArrivalMaxMs               int
	HaircutMinMs               int
	HaircutMaxMs               int
	SatisfactionWaitThresholdS float64
	ShutdownGraceMs            int
	MailboxBuffer              int
}

var cfg = Config{
	TotalCustomers:             20,
	WaitingRoomCapacity:        5,
	ArrivalMinMs:               500,
	ArrivalMaxMs:               2000,
	HaircutMinMs:               1000,
	HaircutMaxMs:               4000,
	SatisfactionWaitThresholdS: 3.0,
	ShutdownGraceMs:            8000,
	MailboxBuffer:              32,
}

type MsgKind int

const (
	MsgArrive MsgKind = iota
	MsgAdmitted
	MsgTurnedAway
	MsgNextCustomer
	MsgCustomerReady
	MsgNoneWaiting
	MsgWakeUp
	MsgRateRequest
	MsgRating
	MsgGetStats
	MsgStatsReply
	MsgShutdown
	MsgCalledByBarber
	MsgInitWaitingRoom
)

type StatsPayload struct {
	Role              string
	TurnedAway        int
	QueueLength       int
	HaircutsCompleted int
	AvgDurationMs     float64
	AvgRating         float64
}

type Message struct {
	Kind       MsgKind
	From       chan Message
	CustomerID int
	Value      int
	ArrivalMs  int64
	QueueDepth int
	Stats      StatsPayload
}

type Actor chan Message

func newActor() Actor {
	return make(chan Message, cfg.MailboxBuffer)
}

var simStart time.Time

func elapsedMs() int64 {
	return time.Since(simStart).Milliseconds()
}

func logf(who, format string, args ...any) {
	fmt.Printf("[%d ms] %s: %s\n", elapsedMs(), who, fmt.Sprintf(format, args...))
}

func randBetween(min, max int) int {
	return rand.Intn(max-min+1) + min
}

func clamp(val, low, high int) int {
	if val < low {
		return low
	}
	if val > high {
		return high
	}
	return val
}

func avgAdd(oldAvg float64, countBefore int, newVal float64) float64 {
	return ((oldAvg * float64(countBefore)) + newVal) / float64(countBefore+1)
}

func customer(id int, waitingRoom Actor) {
	mailbox := newActor()
	arrivalMs := elapsedMs()

	logf(fmt.Sprintf("Customer %d", id), "arrives")

	waitingRoom <- Message{
		Kind:       MsgArrive,
		From:       mailbox,
		CustomerID: id,
		ArrivalMs:  arrivalMs,
	}

	for {
		msg := <-mailbox
		switch msg.Kind {
		case MsgTurnedAway:
			logf(fmt.Sprintf("Customer %d", id), "turned away and exits")
			return

		case MsgAdmitted:
			logf(fmt.Sprintf("Customer %d", id), "admitted to waiting room, queue depth %d", msg.QueueDepth)

		case MsgCalledByBarber:
			waitMs := elapsedMs() - arrivalMs
			logf(fmt.Sprintf("Customer %d", id), "called by barber after waiting %d ms", waitMs)

		case MsgRateRequest:
			waitMs := elapsedMs() - arrivalMs
			thresholdMs := int(cfg.SatisfactionWaitThresholdS * 1000.0)
			starsLost := 0
			if thresholdMs > 0 {
				starsLost = int(waitMs) / thresholdMs
			}
			jitter := rand.Intn(3) - 1
			rating := clamp(5-starsLost+jitter, 1, 5)
			logf(fmt.Sprintf("Customer %d", id), "gives rating %d after waiting %d ms", rating, waitMs)

			msg.From <- Message{
				Kind:       MsgRating,
				From:       mailbox,
				CustomerID: id,
				Value:      rating,
			}
			return
		}
	}
}

type WaitingRoomState struct {
	queue          []Message
	turnedAway     int
	barberSleeping bool
	barber         Actor
	owner          Actor
}

func waitingRoom(mailbox Actor, barber Actor) {
	state := WaitingRoomState{
		queue:          make([]Message, 0),
		turnedAway:     0,
		barberSleeping: false,
		barber:         barber,
	}

	for {
		msg := <-mailbox

		switch msg.Kind {
		case MsgArrive:
			if len(state.queue) >= cfg.WaitingRoomCapacity {
				msg.From <- Message{Kind: MsgTurnedAway}
				state.turnedAway++
				logf("WaitingRoom", "turns away Customer %d; queue depth %d", msg.CustomerID, len(state.queue))
			} else {
				state.queue = append(state.queue, msg)
				depth := len(state.queue)
				msg.From <- Message{Kind: MsgAdmitted, QueueDepth: depth}
				logf("WaitingRoom", "admits Customer %d; queue depth %d", msg.CustomerID, depth)

				if state.barberSleeping {
					state.barber <- Message{Kind: MsgWakeUp}
					state.barberSleeping = false
					logf("WaitingRoom", "wakes barber")
				}
			}

		case MsgNextCustomer:
			if len(state.queue) > 0 {
				next := state.queue[0]
				state.queue = state.queue[1:]
				msg.From <- Message{
					Kind:       MsgCustomerReady,
					From:       next.From,
					CustomerID: next.CustomerID,
				}
				logf("WaitingRoom", "sends Customer %d to barber", next.CustomerID)
			} else {
				msg.From <- Message{Kind: MsgNoneWaiting}
				state.barberSleeping = true
				logf("WaitingRoom", "no customers waiting; barber should sleep")
			}

		case MsgGetStats:
			msg.From <- Message{
				Kind: MsgStatsReply,
				Stats: StatsPayload{
					Role:        "waiting_room",
					TurnedAway:  state.turnedAway,
					QueueLength: len(state.queue),
				},
			}

		case MsgShutdown:
			logf("WaitingRoom", "shutdown received; exiting")
			return
		}
	}
}

type BarberState struct {
	haircutsCompleted int
	avgDurationMs     float64
	avgRating         float64
	waitingRoom       Actor
}

func barber(mailbox Actor, waitingRoom Actor) {
	state := BarberState{
		haircutsCompleted: 0,
		avgDurationMs:     0.0,
		avgRating:         0.0,
		waitingRoom:       waitingRoom,
	}

	logf("Barber", "starts up")
	waitingRoom <- Message{Kind: MsgNextCustomer, From: mailbox}

	for {
		msg := <-mailbox

		switch msg.Kind {
		case MsgNoneWaiting:
			logf("Barber", "goes to sleep")

			sleeping := true
			for sleeping {
				select {
				case s := <-mailbox:
					switch s.Kind {
					case MsgWakeUp:
						logf("Barber", "wakes up")
						waitingRoom <- Message{Kind: MsgNextCustomer, From: mailbox}
						sleeping = false

					case MsgGetStats:
						s.From <- Message{
							Kind: MsgStatsReply,
							Stats: StatsPayload{
								Role:              "barber",
								HaircutsCompleted: state.haircutsCompleted,
								AvgDurationMs:     state.avgDurationMs,
								AvgRating:         state.avgRating,
							},
						}

					case MsgShutdown:
						logf("Barber", "shutdown received while sleeping; exiting")
						return

					case MsgCustomerReady:
						mailbox <- s
						sleeping = false
					}
				}
			}

		case MsgCustomerReady:
			customerMailbox := msg.From
			customerID := msg.CustomerID

			haircutMs := randBetween(cfg.HaircutMinMs, cfg.HaircutMaxMs)
			logf("Barber", "starts haircut for Customer %d; planned duration %d ms", customerID, haircutMs)

			customerMailbox <- Message{
				Kind: MsgCalledByBarber,
				From: mailbox,
			}

			time.Sleep(time.Duration(haircutMs) * time.Millisecond)

			logf("Barber", "finishes haircut for Customer %d; asks for rating", customerID)
			customerMailbox <- Message{
				Kind: MsgRateRequest,
				From: mailbox,
			}

			for {
				rmsg := <-mailbox
				if rmsg.Kind == MsgRating && rmsg.CustomerID == customerID {
					countBefore := state.haircutsCompleted
					state.avgDurationMs = avgAdd(state.avgDurationMs, countBefore, float64(haircutMs))
					state.avgRating = avgAdd(state.avgRating, countBefore, float64(rmsg.Value))
					state.haircutsCompleted++

					logf(
						"Barber",
						"received rating %d; cuts=%d, avg_duration=%.2fs, avg_rating=%.2f",
						rmsg.Value,
						state.haircutsCompleted,
						state.avgDurationMs/1000.0,
						state.avgRating,
					)

					waitingRoom <- Message{Kind: MsgNextCustomer, From: mailbox}
					break
				} else if rmsg.Kind == MsgShutdown {
					logf("Barber", "shutdown received after completing haircut; exiting")
					return
				} else if rmsg.Kind == MsgGetStats {
					rmsg.From <- Message{
						Kind: MsgStatsReply,
						Stats: StatsPayload{
							Role:              "barber",
							HaircutsCompleted: state.haircutsCompleted,
							AvgDurationMs:     state.avgDurationMs,
							AvgRating:         state.avgRating,
						},
					}
				}
			}

		case MsgGetStats:
			msg.From <- Message{
				Kind: MsgStatsReply,
				Stats: StatsPayload{
					Role:              "barber",
					HaircutsCompleted: state.haircutsCompleted,
					AvgDurationMs:     state.avgDurationMs,
					AvgRating:         state.avgRating,
				},
			}

		case MsgShutdown:
			logf("Barber", "shutdown received; exiting")
			return
		}
	}
}

func shopOwner() {
	ownerMailbox := newActor()
	barberMailbox := newActor()
	waitingRoomMailbox := newActor()

	go barber(barberMailbox, waitingRoomMailbox)
	go waitingRoom(waitingRoomMailbox, barberMailbox)

	total := cfg.TotalCustomers

	for i := 1; i <= total; i++ {
		logf("ShopOwner", "spawns Customer %d of %d", i, total)
		go customer(i, waitingRoomMailbox)

		if i < total {
			time.Sleep(time.Duration(randBetween(cfg.ArrivalMinMs, cfg.ArrivalMaxMs)) * time.Millisecond)
		}
	}

	logf("ShopOwner", "all customers spawned; waiting grace period %d ms", cfg.ShutdownGraceMs)
	time.Sleep(time.Duration(cfg.ShutdownGraceMs) * time.Millisecond)

	waitingRoomMailbox <- Message{Kind: MsgGetStats, From: ownerMailbox}
	barberMailbox <- Message{Kind: MsgGetStats, From: ownerMailbox}

	var barberStats StatsPayload
	var wrStats StatsPayload
	gotBarber := false
	gotWR := false

	for !(gotBarber && gotWR) {
		msg := <-ownerMailbox
		if msg.Kind == MsgStatsReply {
			if msg.Stats.Role == "barber" {
				barberStats = msg.Stats
				gotBarber = true
			} else if msg.Stats.Role == "waiting_room" {
				wrStats = msg.Stats
				gotWR = true
			}
		}
	}

	barberMailbox <- Message{Kind: MsgShutdown}
	waitingRoomMailbox <- Message{Kind: MsgShutdown}
	logf("ShopOwner", "shutdown initiated")

	time.Sleep(200 * time.Millisecond)

	fmt.Println()
	fmt.Println("=== Barbershop Closing Report ===")
	fmt.Printf("Total customers arrived: %d\n", total)
	fmt.Printf("Customers served: %d\n", barberStats.HaircutsCompleted)
	fmt.Printf("Customers turned away: %d\n", wrStats.TurnedAway)
	fmt.Printf("Average haircut duration: %.2fs\n", barberStats.AvgDurationMs/1000.0)
	fmt.Printf("Average satisfaction: %.2f / 5.0\n", math.Round(barberStats.AvgRating*100)/100)
	fmt.Println("=================================")
}

func main() {
	rand.Seed(time.Now().UnixNano())
	simStart = time.Now()
	shopOwner()
}