import websocket
import threading
import time
import requests
import json

# ================= 靶场配置 =================
BASE_URL_HTTP = "http://localhost:8080"
BASE_URL_WS = "ws://localhost:8080/api/v1/ws"
USERNAME = "test05"
PASSWORD = "123456"

# 1. 第一阶段：用老办法去搞护照 (复用之前的逻辑)
def get_token():
    print(" [HTTP 侦察] 正在向 Go 网关申请护照...")
    res = requests.post(f"{BASE_URL_HTTP}/api/v1/user/login", json={"username": USERNAME, "password": PASSWORD})
    if res.status_code != 200:
        print(f" 登录失败: {res.status_code}")
        return None
    data = res.json()
    token = data.get("token") or (data.get("data") and data["data"].get("token"))
    if token:
        if not token.startswith("Bearer "):
            token = f"Bearer {token}"
        print(f" 成功获取护照: {token[:20]}...\n")
        return token
    return None

# ================= 核心：WebSocket 事件钩子 =================

def on_message(ws, message):
    """网关主动推送消息时，触发此雷达"""
    print(f" [战壕接收] 网关发来报文: {message}")

def on_error(ws, error):
    """底层 TCP 断裂或报错时触发"""
    print(f" [物理异常] {error}")

def on_close(ws, close_status_code, close_msg):
    """通道被强制关闭时触发"""
    print(f" [通道关闭] 物理连接已断开 (状态码: {close_status_code})")

def on_open(ws):
    """握手成功，战壕挖通的一瞬间触发！"""
    print(" [通道建立] WebSocket 物理握手成功，全双工阵地已部署！")
    
    #  架构师核心操作：起一个幽灵子线程去异步开火，绝不能阻塞主线程的监听！
    def fire_machine_gun():
        print(" [机枪手就位] 准备进行连续点射...")
        for i in range(1, 4):  # 连开 3 枪
            time.sleep(1) # 模拟人类打字停顿
            
            #  注意：这里必须严格对齐你 Go 网关期望接收的 JSON 格式
            # 假设你的网关期望收到 receiver_id 和 content
            payload = {
                "receiver_id": "user_002",
                "content": f"这是通过 WS 长连接打出的第 {i} 发子弹！"
            }
            msg = json.dumps(payload, ensure_ascii=False)
            print(f" [发射子弹] -> {msg}")
            ws.send(msg)
            
        print(" 弹药打光，5秒后主动炸毁战壕...")
        time.sleep(5)
        ws.close() # 打完收工，主动切断连接

    # 启动异步开火线程
    threading.Thread(target=fire_machine_gun).start()

if __name__ == "__main__":
    jwt_token = get_token()
    
    if jwt_token:
        # 1. 提取纯净 Token
        clean_token = jwt_token.replace("Bearer ", "")
        
        # 2. 【车牌注入】挂载 URL 坐标
        TARGET_URL = f"{BASE_URL_WS}?token={clean_token}"
        
        # 3. 【口袋注入】强行塞入标准 Header 护照
        # 注意：这里我们把 Bearer 重新拼上去，满足标准 JWT 中间件的胃口！
        headers = [f"Authorization: Bearer {clean_token}"]
        
        print(f"📡 正在尝试建立 WebSocket 长连接通道...\n目标靶位: {TARGET_URL}")
        
        # 4. 双管齐下，火力全开
        ws_app = websocket.WebSocketApp(
            TARGET_URL,
            header=headers,     # ⚡ 头和 URL，我全都要！
            on_open=on_open,
            on_message=on_message,
            on_error=on_error,
            on_close=on_close
        )
        
        ws_app.run_forever()