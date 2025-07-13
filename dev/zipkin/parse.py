import json
from datetime import datetime
import time
import requests
import csv

# services = ["first", "second", "third", "sequence-display"]
services = ["validate-fun", "vote-fun", "count-vote-fun", "display-fun"]
# services = ["validate-fun-stock", "vote-fun-stock", "count-vote-fun-stock", "display-fun-stock"]
# services = ["validate-fun-member", "vote-fun-member", "count-vote-fun-member", "display-fun-member"]

def parse_trace_timings(trace):
    sorted_d = sorted(trace, key=lambda x: x['timestamp'])
    
    # now look for 
    # kind: SERVER, localEndpoint.ServiceName: activator-service and 
    # tags.http.host: first.default.svc.cluster.local
        
    server_only = [x for x in sorted_d if 'kind' in x and x['kind'] == 'SERVER']
    activator_service = [x for x in server_only if x['localEndpoint']['serviceName'] == 'activator-service']
    
    row = {}
    for service in activator_service:
        tags = service['tags']
        host = tags['http.host']
        status_code = tags["http.status_code"]
        if status_code != "200":
            print(f"Warning: non-200 status code {status_code} for host {host}")
            return None
        
        service_name = host.split('.')[0]
        
        # duration is in microseconds, convert to milliseconds
        row[service_name] = service["duration"] / 1000
    
    row['trace_id'] = trace[0]['traceId']
    
    return row

def parse_trace_file(filename='trace.json'):
    with open('trace.json', 'r') as f:
        trace = json.load(f)
        print(parse_trace_timings(trace))

def main():
    service_name = "activator-service"
    limit = 100000
    # look_back = "7d"
    look_back = 604800000 # 7 days in milliseconds
    # in milliseconds
    end_timestamp = int(time.time() * 1000)
    url = f"http://localhost:8001/api/v1/namespaces/zipkin-monitoring/services/zipkin:9411/proxy/zipkin/api/v2/traces?serviceName={service_name}&lookback={look_back}&endTs={end_timestamp}&limit={limit}"
    
    response = requests.get(url)

    if response.status_code == 200:
        data = response.json()
        
        trace_count = len(data)
        print(f"Found {trace_count} traces")
        
        with open("all_traces.json", "w") as json_file:
            json.dump(data, json_file, indent=4)
            
        rows = []
        for trace in data:
            row = parse_trace_timings(trace)
            if row:
                rows.append(row)
        
        with open('service_traces.csv', 'w', newline='') as file:
            writer = csv.writer(file)
            first_row = ["trace_id"] + services
            writer.writerow(first_row)
            
            for row in rows:
                ordered_row = [row.get(service, "") for service in services]
                ordered_row.insert(0, row['trace_id'])
                writer.writerow(ordered_row)
    else:
        print(f"Failed status code: {response.status_code}, text: {response.text}")
    
    
if __name__ == "__main__":
    main()    
    