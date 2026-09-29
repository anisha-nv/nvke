# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import http.server,json,os,pathlib,subprocess,tempfile,threading,unittest
SCRIPT=str(pathlib.Path(__file__).with_name('demo.sh'))
TARGET='11111111-1111-4111-8111-111111111111'
SENTINEL='22222222-2222-4222-8222-222222222222'
MOCK=r'''#!/usr/bin/env python3
import json,os,pathlib,sys,urllib.request,urllib.error
root=pathlib.Path(os.environ['DEMO_FIXTURE']); state=root/'staged'; args=sys.argv[1:]; tool=pathlib.Path(sys.argv[0]).name
rows=[{'id':'11111111-1111-4111-8111-111111111111','name':'target','conditions':{'provisioned':{'status':'True'},'apiServerReachable':{'status':'False' if state.exists() else 'True','message':'fixture','lastTransitionTime':'2026-09-28T00:00:00Z'}}},{'id':'22222222-2222-4222-8222-222222222222','name':'sentinel','conditions':{'provisioned':{'status':'True'},'apiServerReachable':{'status':'True','message':'fixture','lastTransitionTime':'2026-09-28T00:00:00Z'}}}]
if tool=='nkectl':
 if args[:2]==['config','view']: print(json.dumps({'sites':{'fixture':{'url':os.environ['DEMO_URL']}}}));sys.exit()
 if '--site' in args and args[args.index('--site')+1]=='denied':
  cfg=json.loads((pathlib.Path(os.environ['XDG_CONFIG_HOME'])/'nkectl/config.yaml').read_text())
  try: urllib.request.urlopen(cfg['sites']['denied']['url']+'/api/clusters');sys.exit(0)
  except urllib.error.HTTPError:sys.exit(1)
 if '-o' in args: print(json.dumps(rows))
 else:
  print('NAME KUBERNETES VERSION CNI STATUS ID AGE API REACHABLE')
  for r in rows: print(r['name'],'1.34.2','calico','Provisioned',r['id'],'1h',r['conditions']['apiServerReachable']['status'])
elif tool=='docker':
 if args[0]=='inspect':print('target')
 elif 'mv' in args:state.touch()
 elif 'sh' in args:
  if os.environ.get('DEMO_RESTORE_FAIL')=='1':sys.exit(1)
  state.unlink(missing_ok=True)
 elif '!' in args and state.exists():sys.exit(1)
elif tool=='kubectl':
 if 'clusters.cluster.x-k8s.io' in args: print(json.dumps({'items':[{'metadata':{'name':'target','namespace':'default','annotations':{'site-health.nke.nvidia.com/disposable':'true'}},'spec':{'infrastructureRef':{'kind':'DockerCluster'}}}]}))
 else:print(json.dumps({'items':[{'status':{'nodeRef':{'name':'cp'}}}]}))
elif tool=='sleep':pass
elif tool=='yq':print(sys.stdin.read())
'''
class TestDemo(unittest.TestCase):
 def exercise(self,fault=False,forced=False,restore_fail=False):
  with tempfile.TemporaryDirectory() as directory:
   root=pathlib.Path(directory)
   for tool in ['nkectl','docker','kubectl','sleep','yq']:
    (root/tool).write_text(MOCK);(root/tool).chmod(0o755)
   (root/'token').write_text('example-token')
   class API(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
     if self.headers.get('Authorization')!='Bearer example-token': self.send_response(401);self.end_headers();return
     env={**os.environ,'DEMO_FIXTURE':directory}
     rows=json.loads(subprocess.check_output([str(root/'nkectl'),'get','clusters','-o','json'],env=env))
     self.send_response(200);self.end_headers();self.wfile.write(json.dumps({'clusters':rows}).encode())
    def log_message(self,*args):pass
   server=http.server.ThreadingHTTPServer(('127.0.0.1',0),API)
   thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
   try:
    args=['bash',SCRIPT,'--site','fixture','--token-file',str(root/'token')]
    if fault: args+=['--inject-outage','--target',TARGET,'--sentinel',SENTINEL,'--disposable-target','target','--management-context','fixture']
    env={**os.environ,'PATH':directory+':'+os.environ['PATH'],'DEMO_FIXTURE':directory,'DEMO_URL':f'http://127.0.0.1:{server.server_port}','SITE_HEALTH_DEMO_FAIL_AFTER_STAGING':str(forced).lower(),'DEMO_RESTORE_FAIL':'1' if restore_fail else '0'}
    result=subprocess.run(args,env=env,capture_output=True,text=True,timeout=45)
    if forced or restore_fail:self.assertNotEqual(result.returncode,0,result.stdout+result.stderr)
    else:self.assertEqual(result.returncode,0,result.stdout+result.stderr)
    if fault:
     self.assertEqual((root/'staged').exists(),restore_fail,result.stdout+result.stderr)
     self.assertIn('FAIL: cleanup recovery' if restore_fail else 'PASS: cleanup restored target API reachability',result.stdout+result.stderr)
    else:self.assertIn('SKIP: outage',result.stdout)
    print(result.stdout+result.stderr,end='')
   finally:server.shutdown();server.server_close();thread.join()
 def test_observation(self):self.exercise()
 def test_outage(self):self.exercise(fault=True)
 def test_assertion_restores(self):self.exercise(fault=True,forced=True)
 def test_restore_failure_fails(self):self.exercise(fault=True,forced=True,restore_fail=True)
if __name__=='__main__':unittest.main()
