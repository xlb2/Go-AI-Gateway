// Browser fixtures only; no credentials, model requests or database access.
let libraries = [{id:1,name:'Agent 工程笔记'},{id:2,name:'Go 并发与系统设计'}];
const sources = [{id:1,library_id:1,title:'Harness：循环与工具执行.md',format:'md'},{id:2,library_id:1,title:'上下文压缩与恢复.txt',format:'txt'}];
const evidence = {event_id:2,turn_id:1,source_id:1,version_id:1,version:1,title:'Harness：循环、工具执行与恢复边界的原始资料.md',offset:0,next_offset:31,content:'每一步保存执行状态。\n<script>仅为原文</script>'};
evidence.next_offset = Array.from(evidence.content).length;
const {content:excerpt, ...evidenceRef} = evidence;
const conversations = [{id:1,library_id:1,title:'工具执行的依据',turns:[{id:1,request_id:'fixture-reading',question:'工具执行的依据是什么？',answer:'每一步都应保存执行状态，出现异常时不能伪装成功。',status:'completed',sources_json:'[]',evidence:[evidenceRef]}]}];
let turnID = 1;
module.exports = (req, res) => {
  const url = new URL(req.url, 'http://localhost');
  const json = (data, code=200) => {res.writeHead(code,{'Content-Type':'application/json'});res.end(JSON.stringify(data));};
  if (url.pathname === '/api/v1/libraries/1/conversations/1/turns/1/evidence/2') { json(evidence); return; }
  if (url.pathname === '/api/v1/user/login') {req.resume();json({token:'ui-fixture-token'});return;}
  if (url.pathname === '/api/v1/libraries') {
    if (req.method === 'POST') {
      let body='';req.on('data',chunk=>body+=chunk);req.on('end',()=>{const lib={id:libraries.length+1,name:JSON.parse(body).name};libraries.push(lib);json(lib);});return;
    }
    json({items:libraries});return;
  }
  const convPath=url.pathname.match(/\/libraries\/(\d+)\/conversations(?:\/(\d+))?(\/turns)?$/);
  if(convPath){
    const lib=Number(convPath[1]),id=Number(convPath[2]);
    if(!id){
      req.resume();
      if(req.method==='POST'){const conv={id:conversations.length+1,library_id:lib,title:'新对话',turns:[]};conversations.push(conv);json(conv,201);return;}
      json({items:conversations.filter(c=>c.library_id===lib).slice().reverse()});return;
    }
    const conv=conversations.find(c=>c.id===id && c.library_id===lib);
    if(!conv){json({error:'not found'},404);return;}
    if(!convPath[3]){json({conversation:{id:conv.id,library_id:lib,title:conv.title},turns:conv.turns});return;}
    let body='';req.on('data',chunk=>body+=chunk);req.on('end',()=>{
      const input=JSON.parse(body),existing=conv.turns.find(t=>t.request_id===input.request_id);
      if(existing){json({replayed:true,turn:existing});return;}
      const turn={id:++turnID,request_id:input.request_id,question:input.content,answer:'',status:'running',sources_json:JSON.stringify(input.source_ids.map(id=>({...sources.find(s=>s.id===id),version:1})))};
      conv.turns.push(turn);conv.title=conv.turns[0].question.slice(0,60);
      res.writeHead(200,{'Content-Type':'application/x-ndjson'});res.write(JSON.stringify({type:'started',turn_id:turn.id})+'\n');
      const parts=['Agent 循环以一次模型调用为一个 step。\n\n','模型选择下一步动作，工具负责执行。工具结果进入下一步上下文，直到产生最终回答。\n\n','对话记录与一次运行的状态需要分别保存；取消或异常不能冒充成功。'];
      let i=0;
      const timer=setInterval(()=>{if(i<parts.length){turn.answer+=parts[i];res.write(JSON.stringify({type:'delta',content:parts[i++]})+'\n');}else{turn.status='completed';res.end(JSON.stringify({type:'done',turn_id:turn.id})+'\n');clearInterval(timer);}},300);
      res.on('close',()=>{clearInterval(timer);if(turn.status==='running')turn.status='canceled';});
    });return;
  }
  const match=url.pathname.match(/\/libraries\/(\d+)\/sources(?:\/(\d+))?$/);
  if(match){
    const lib=Number(match[1]),id=Number(match[2]);
    if(req.method==='POST'){req.resume();const src={id:sources.length+1,library_id:lib,title:'browser-upload.txt',format:'txt'};sources.push(src);json({source:src,duplicate:false},201);return;}
    if(id){const src=sources.find(s=>s.id===id && s.library_id===lib);if(!src){json({error:'not found'},404);return;}json({source:src,version:{version:1,body:'# Agent 循环\n\n每一步都需要明确的输入、结果与结束状态。\n\n模型负责决策，执行器负责工具调用。'}});return;}
    json({items:sources.filter(s=>s.library_id===lib)});return;
  }
  json({error:'Fixture route unavailable'},404);
};
