import requests
import time
import json

# ================= 靶场配置 =================
BASE_URL = "http://localhost:8080"
# 直接使用你刚才通过 curl 注册成功的兵力
USERNAME = "test05"
PASSWORD = "123456"

def get_token():
    """阶段一：向 Go 网关申请护照 (带防爆盾的请求)"""
    print(" [阶段一] 正在向 Go 网关发起登录请求...")
    login_url = f"{BASE_URL}/api/v1/user/login"
    payload = {
        "username": USERNAME,
        "password": PASSWORD
    }
    
    try:
        res = requests.post(login_url, json=payload)
        
        #  核心防御：绝对不要无脑 .json()，先检查状态码并用 try 捕获
        try:
            data = res.json()
        except requests.exceptions.JSONDecodeError:
            print(f" 致命异常：Go 后端返回的不是合法 JSON！")
            print(f"状态码: {res.status_code}")
            print(f"原始报文: {res.text}")
            return None

        # 如果 HTTP 状态码不是 200，说明业务逻辑被拦截（如密码错误）
        if res.status_code != 200:
            print(f" 登录请求被网关拒绝:\n状态码: {res.status_code}\n返回报文: {data}")
            return None

        #  智能探测 Token 的存放位置（兼容不同后端的 JSON 结构）
        token = data.get("token")
        if not token and "data" in data and isinstance(data["data"], dict):
            token = data["data"].get("token")
            
        if not token:
            print(f" 登录成功，但在你的 Go 返回值里找不到 'token' 字段！\n完整返回: {data}")
            return None
            
        print(f" 成功获取护照 Token: {token[:20]}...\n")
        
        #  自动拼装 Bearer 前缀（如果你的 JWT 中间件需要的话）
        if not token.startswith("Bearer "):
            token = f"Bearer {token}"
            
        return token
        
    except requests.exceptions.ConnectionError:
        print(" 物理连接失败！请确认你的 Go 网关 (main.go) 正在 8080 端口运行！")
        return None

def test_rate_limiter(token, requests_count=15):
    print(f" [阶段二] 开始执行 {requests_count} 次高频连发轰炸...")
    send_url = f"{BASE_URL}/api/v1/message/send"
    headers = {"Authorization": token, "Content-Type": "application/json"}
    msg_payload = {"receiver_id": "test_receiver", "content": "压测"}

    success_count = 0
    block_count = 0
    error_count = 0
    
    #  1. 在这里建一个空列表，用来装每一次的耗时
    latency_list = []

    for i in range(1, requests_count + 1):
        start_time = time.time() 
        res = requests.post(send_url, headers=headers, json=msg_payload)
        cost_ms = (time.time() - start_time) * 1000 
        
        #  2. 不管成功还是限流，只要有耗时，就把这发子弹的耗时塞进列表
        latency_list.append(cost_ms)
        
        if res.status_code == 200:
            success_count += 1
            # ... 打印成功日志
        elif res.status_code == 429: 
            block_count += 1
            # ... 打印限流日志
        else:
            error_count += 1
            # ... 打印报错日志
            
        time.sleep(0.01) 

    print("\n === 压测火力报告 ===")
    print(f"总发包数: {requests_count}")
    print(f"穿透成功: {success_count} 次")
    print(f"触发限流: {block_count} 次")
    
    #  3. 在这里写一段逻辑：
    # 如果 latency_list 里面有数据（长度大于0），
    # 打印出：最大延迟是多少毫秒？平均延迟是多少毫秒？(保留两位小数)
    max_lat = max(latency_list)
    avg_lat = sum(latency_list)/len(latency_list)
    print(f"最大延迟是:{max_lat:.2f}ms,平均延迟是:{avg_lat:.2f}ms")

if __name__ == "__main__":
    jwt_token = get_token()
    if jwt_token:
        test_rate_limiter(jwt_token, requests_count=15)