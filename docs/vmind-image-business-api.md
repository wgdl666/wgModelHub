# VWorld image v2 forwarding

## 国内 Go 网关

为国内配置单独的 `vmind_gateway` 供应商实例，在 `model_routes` 选择该实例后，新请求使用 Go 网关。保留已受理视频记录引用的原供应商实例，确保查询仍按实际受理归属执行。供应商实例与对象存储配置随 ModelHub 启动加载。

```yaml
providers:
  vmind_go_cn:
    models: [Vmind_wardrobe_v1.0, Vmind_cloth_gen_v1.0, Vmind_outfit_v1.0, Vmind_bodymodel_v1.0, Vmind_outfit_video_v1.0]
    vmind_gateway:
      base_url: http://model-gateway:18580
      poll_interval: 1
      result_storage:
        provider: oss
        region: cn-shenzhen
        bucket: <result-bucket>
        endpoint: <result-endpoint>
model_routes:
  Vmind_wardrobe_v1.0: vmind_go_cn
  Vmind_cloth_gen_v1.0: vmind_go_cn
  Vmind_outfit_v1.0: vmind_go_cn
  Vmind_bodymodel_v1.0: vmind_go_cn
  Vmind_outfit_video_v1.0: vmind_go_cn
```

ACK Secret `model-gateway-modelhub` 注入 `api-token`、`oss-access-key-id` 和 `oss-access-key-secret`。图片同步读取已上传对象，视频以 ModelHub 任务 UUID 向网关提交和查询，再从同一配置的存储桶流式读取成品。

## VMind-Image 直连

调用方在 `GenerateRequest.model` 中选择操作。`Vmind_wardrobe_v1.0` 用于人物拆衣，`Vmind_cloth_gen_v1.0` 用于单件原图生成商品图。直连和 Go 网关使用相同的模型键。

| Model / inputs | Upstream path | Text fields | Upload fields |
|---|---|---|---|
| Vmind_cloth_gen_v1.0, one product image | /v2/images/product_generation | garment_name, garment_role | image |
| Vmind_wardrobe_v1.0, parser payload | /v2/images/garment_extraction | garment_name, garment_role, parse_labels | image, parse_mask |
| Vmind_wardrobe_v1.0, bbox | /v2/images/garment_extraction | garment_name, bbox, optional bbox_format, optional garment_role | image |
| Vmind_outfit_v1.0 | /v2/images/vton | prompt | person_image, garment_image |
| Vmind_bodymodel_v1.0 | /v2/images/body_model | gender | image |

A valid active span adds trace_id. Product metadata comes from the existing JSON text part; `bbox` is `[x1,y1,x2,y2]` on the person image (all values in 0–1 are normalized, otherwise pixels, unless `bbox_format` is `pixel_xyxy` or `normalized_xyxy`) and is forwarded as a JSON array string. An empty `garment_role` is omitted rather than sent as an empty enum value; gender comes from input.model_traits. VTON preserves text-part content and person/garment order. Response decoding remains b64_json → image bytes.

Do not send model, lora_name, lora_strength, size, seed, n, response_format, use_crop, input_mode, or negative_prompt. Do not infer a garment role from inventory_slot or apply role/gender/category validation. Do not enforce crop.applied in this gateway. The model service owns these rules and returns upstream errors before inference. Structural protocol checks (JSON envelope and media mapping) remain here.

切换商品图模型键前，先处理完旧键上的商品图任务。在使用 Go 网关的环境，更新模型侧适配器并按新键登记商品图实例，再将新键加入 ModelHub 路由。ModelHub 和衣橱的切换要安排在同一发布窗口：衣橱开始传新键后，商品图请求才能走新路由。
