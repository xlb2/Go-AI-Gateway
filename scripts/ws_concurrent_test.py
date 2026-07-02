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

def get_token():
    print(" [HTTP 侦察] 正在获取主控护照...")
    res = requests.post(f"{BASE_URL_HTTP}/api/v1/user/login", json={"username": USERNAME, "password": PASSWORD})
    if res.status_code != 200:
        print(f" 登录失败: {res.status_code}")
        return None
    data = res.json()
    return data.get("token") or (data.get("data") and data["data"].get("token"))

# ================= 独立的并发机枪手 =================
def ws_client_thread(thread_id, token):
    clean_token = token.replace("Bearer ", "")
    url = f"{BASE_URL_WS}?token={clean_token}"
    
    def on_message(ws, message):
        pass # 并发模式下静音，防止控制台刷屏卡死
        
    def on_error(ws, error):
        # 拦截其他底层物理异常
        print(f" [线程 {thread_id:02d} 异常] {error}")
        
    def on_close(ws, close_status_code, close_msg):
        pass

    def on_open(ws):
        print(f" [线程 {thread_id:02d}] 战壕建立！开始火力覆盖...")
        for i in range(1, 6):
            payload = {
                "receiver_id": "user_002",
                "content": f"[并发轰炸] 这是线程 {thread_id:02d} 打出的第 {i} 发子弹！"
            }
            
            #  核心护甲：防炸膛物理拦截机制
            try:
                # 释放纯正中文，关闭 ASCII 强转
                msg = json.dumps(payload, ensure_ascii=False)
                ws.send(msg)
            except AttributeError:
                # 专门捕获 'NoneType' object has no attribute 'sock' 的炸膛异常
                print(f" [线程 {thread_id:02d}] 枪管已被网关物理熔毁，射击强制中止！")
                break 
            except Exception as e:
                print(f" [线程 {thread_id:02d}] 未知发射异常: {e}")
                break
                
            time.sleep(0.05) # 极高频射击：50毫秒间隔
            
        time.sleep(1)
        ws.close()

    # 启动单兵雷达
    ws_app = websocket.WebSocketApp(
        url,
        on_open=on_open,
        on_message=on_message,
        on_error=on_error,
        on_close=on_close
    )
    ws_app.run_forever()

# ================= 总指挥部 =================
if __name__ == "__main__":
    jwt_token = get_token()
    if not jwt_token:
        exit(1)
        
    print("\n [全军出击] 准备释放 10 线程并发集束炸弹...\n")
    threads = []
    
    for i in range(1, 11):
        t = threading.Thread(target=ws_client_thread, args=(i, jwt_token))
        threads.append(t)
        t.start()
        
    for t in threads:
        t.join() # 阻塞主线程，等待所有子线程撤离
        
    print("\n [战役结束] 并发连射指令已全部执行完毕。")