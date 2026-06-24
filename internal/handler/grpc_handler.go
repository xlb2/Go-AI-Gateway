package handler

import (
	"go_im_gateway/rpc"
	"io"
	"log"
)

type AgentGatewayImpl struct {
	rpc.UnimplementedAgentGatewayServer
}

func (s *AgentGatewayImpl) StreamChat(stream rpc.AgentGateway_StreamChatServer) error {
	log.Println("gRPC 双向流链接已经建立!")
	var seq int32 = 0
	for {
		in, err := stream.Recv()
		if err == io.EOF {
			log.Println("客户端主动断开了连接")
			return nil
		}
		if err != nil {
			log.Printf("接收流出错: %v\n", err)
			return err
		}
		log.Printf("收到客户端消息：Session = %s ,Query = %s\n", in.SessionId, in.UserQuery)
		seq++
		resp := &rpc.ChatResponse{
			SeqNum:    seq,
			DeltaText: "网关回声：" + in.UserQuery,
		}
		err = stream.Send(resp)
		if err != nil {
			log.Printf("发送流出错: %v\n", err)
			return err
		}
	}
}
