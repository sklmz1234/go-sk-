package repository

import (
	"context"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	productpb "go-ecom-admin/proto/product"
)

// ProductClient 是 order-service 对 product-service 的依赖抽象。

type ProductClient interface {
	// DeductStock 成功时返回扣减结果（含 price_cents/name 快照）。
	DeductStock(ctx context.Context, productID uint64, quantity int32) (*productpb.DeductStockResponse, error)

	RestoreStock(ctx context.Context, productID uint64, quantity int32, messageID string) error
}

// defaultCallTimeout 与 api-gateway 的下游调用超时保持一致。
const defaultCallTimeout = 3 * time.Second

type grpcProductClient struct {
	client productpb.ProductServiceClient
}

func NewProductClient(target string) (*grpcProductClient, error) {
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	)
	if err != nil {
		return nil, err
	}
	return &grpcProductClient{client: productpb.NewProductServiceClient(conn)}, nil
}

func (c *grpcProductClient) DeductStock(ctx context.Context, productID uint64, quantity int32) (*productpb.DeductStockResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	return c.client.DeductStock(ctx, &productpb.DeductStockRequest{
		ProductId: productID,
		Quantity:  quantity,
	})
}

func (c *grpcProductClient) RestoreStock(ctx context.Context, productID uint64, quantity int32, messageID string) error {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	_, err := c.client.RestoreStock(ctx, &productpb.RestoreStockRequest{
		ProductId: productID,
		Quantity:  quantity,
		MessageId: messageID,
	})
	return err
}
