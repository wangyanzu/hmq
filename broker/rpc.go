package broker

import (
	"context"
	"net"
	"time"

	"github.com/eclipse/paho.mqtt.golang/packets"
	pb "github.com/fhmq/hmq/grpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
)

func (b *Broker) initRPCService() {
	lis, err := net.Listen("tcp", ":"+b.config.RpcPort)
	if err != nil {
		log.Error("failed to listen: ", zap.Error(err))
		return
	}

	s := grpc.NewServer(grpc.KeepaliveParams(keepalive.ServerParameters{
		Time: 30 * time.Minute,
	}))
	pb.RegisterHMQServiceServer(s, &HMQ{b: b})
	reflection.Register(s)
	if err := s.Serve(lis); err != nil {
		log.Error("failed to server: ", zap.Error(err))
	}
}

func (b *Broker) initRPCClient(id, url string) {

	conn, err := grpc.Dial(url,
		grpc.WithInsecure(),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time: 30 * time.Minute,
		}))
	var tempDelay time.Duration = 0
	var maxRetry int = 0
	for err != nil {
		//max retry
		if maxRetry > 100 {
			b.DeleteNode(id)
			return
		}

		log.Error("create connect rpc service failed", zap.String("url", url), zap.Error(err))
		if 0 == tempDelay {
			tempDelay = 1 * time.Second
		} else {
			tempDelay *= 2
		}

		if max := 20 * time.Second; tempDelay > max {
			tempDelay = max
		}
		time.Sleep(tempDelay)
		log.Debug("connect to rpc timeout, retry...")
		maxRetry++

		conn, err = grpc.Dial(url,
			grpc.WithInsecure(),
			grpc.WithKeepaliveParams(keepalive.ClientParameters{
				Time: 30 * time.Minute,
			}))
	}

	cli := pb.NewHMQServiceClient(conn)
	b.rpcClient[id] = cli
}

type HMQ struct {
	b *Broker
}

func (h *HMQ) QuerySubscribe(ctx context.Context, in *pb.QuerySubscribeRequest) (*pb.Response, error) {
	resp := &pb.Response{
		RetCode: 0,
	}
	topic := in.Topic
	qos := in.Qos
	if qos > 1 {
		resp.RetCode = 404
		return resp, nil
	}

	b := h.b
	var subs []interface{}
	var qoss []byte
	err := b.topicsMgr.Subscribers([]byte(topic), byte(qos), &subs, &qoss)
	if err != nil {
		log.Error("search sub client error,  ", zap.Error(err))
		resp.RetCode = 404
	}

	if len(subs) == 0 {
		resp.RetCode = 404
	}

	return resp, nil
}

func (h *HMQ) QueryConnect(ctx context.Context, in *pb.QueryConnectRequest) (*pb.Response, error) {
	resp := &pb.Response{
		RetCode: 0,
	}

	b := h.b
	cli, exist := b.clients.Load(in.ClientID)
	if exist {
		client := cli.(*client)
		client.Close()
	}

	return resp, nil
}

func (h *HMQ) DeliverMessage(ctx context.Context, in *pb.DeliverMessageRequest) (*pb.Response, error) {
	b := h.b
	p := packets.NewControlPacket(packets.Publish).(*packets.PublishPacket)
	p.TopicName = in.Topic
	p.Payload = in.Payload
	p.Retain = false
	b.PublishMessage(p, false)

	resp := &pb.Response{
		RetCode: 0,
	}
	return resp, nil
}

func (h *HMQ) QueryShareSubscribe(ctx context.Context, in *pb.QueryShareSubscribeRequest) (*pb.ShareSubscribeResponse, error) {
	resp := &pb.ShareSubscribeResponse{
		RetCode: 0,
	}
	topic := in.Topic
	qos := in.Qos
	if qos > 1 {
		resp.RetCode = 404
		return resp, nil
	}

	b := h.b
	var qoss []byte
	var subs []interface{}
	err := b.topicsMgr.Subscribers([]byte(topic), byte(qos), &subs, &qoss)
	if err != nil {
		log.Error("search sub client error,  ", zap.Error(err))
		resp.RetCode = 404
	}

	if len(subs) == 0 {
		resp.RetCode = 404
	}

	var qsub int32
	for _, sub := range subs {
		s, ok := sub.(*subscription)
		if ok {
			if s.share {
				qsub++
			}
		}
	}
	resp.ShareSubCount = qsub
	return resp, nil
}

func (b *Broker) DeliverMessage(packet *packets.PublishPacket) {
	for _, client := range b.rpcClient {

		resp, err := client.QuerySubscribe(context.Background(), &pb.QuerySubscribeRequest{Topic: packet.TopicName, Qos: int32(packet.Qos)})
		if err != nil {
			log.Error("rpc request error:", zap.Error(err))
			continue
		}

		if resp.RetCode == 0 {
			client.DeliverMessage(context.Background(), &pb.DeliverMessageRequest{Topic: packet.TopicName, Payload: packet.Payload})
		}

	}
}

func (b *Broker) QueryConnect(clientID string) {
	for _, client := range b.rpcClient {
		client.QueryConnect(context.Background(), &pb.QueryConnectRequest{ClientID: clientID})
	}
}

func (b *Broker) QueryShareSubscribe(topic string, qos byte) map[string]int32 {
	result := make(map[string]int32)
	for id, client := range b.rpcClient {
		resp, err := client.QueryShareSubscribe(context.Background(), &pb.QueryShareSubscribeRequest{Topic: topic, Qos: int32(qos)})
		if err != nil {
			log.Error("rpc request error:", zap.Error(err))
			continue
		}
		if resp.ShareSubCount > 0 {
			result[id] = resp.ShareSubCount
		}

	}
	return result
}
