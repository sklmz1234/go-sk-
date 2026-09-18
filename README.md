# go-sk-

独立开发 Go 微服务电商系统，采用 API Gateway + gRPC 拆分用户、商品和订单领域；通过条件更新防止库存超卖，并实现 Saga 补偿、事务 Outbox、幂等消费及失败退避；接入 Redis/Elasticsearch 降级、熔断限流和 OpenTelemetry/Prometheus 可观测性。
