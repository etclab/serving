import csv
import statistics as stat

trace_files = [
    'baseline_10rps_10m.csv', 'both_10rps_10m.csv', 'leader_10rps_10m.csv',  'member_10rps_10m.csv'
]

def find_stat(all):
    all.sort()
    
    skip = int(len(all) * 0.1)
    # ignore the first and last 10%
    tall = all[skip:-skip]
    
    min_val = min(tall)
    max_val = max(tall)
    mean = stat.mean(tall)
    std = stat.stdev(tall)
    median = stat.median(tall)
    
    return {
        'min': min_val,
        'max': max_val,
        'mean': mean,
        'std': std,
        'median': median
    }
    
def latency_stat_writer(file_ptr, data, func_name):
    file_ptr.write(f"{f'{func_name}':<25} {data['min']:<25} {data['max']:<25} {data['mean']:<25} {data['median']:<25} {data['std']:<25}\n")
    
def parse_trace_file(in_file, out_file):
    with open(in_file, 'r') as file:
        csv_reader = csv.reader(file)

        header = next(csv_reader)
        
        # take the first 300 records
        count = 0
        rows = []
        for row in csv_reader:
            rows.append(row)
            count += 1
            if count >= 300:
                break
            
        validate_timings = [float(row[1]) for row in rows]
        vote_timings = [float(row[2]) for row in rows]
        count_vote_timings = [float(row[3]) for row in rows]
        display_timings = [float(row[4]) for row in rows]
            
            
        validate_stat = find_stat(validate_timings)
        vote_stat = find_stat(vote_timings)
        count_vote_stat = find_stat(count_vote_timings)
        display_stat = find_stat(display_timings)
        
        print(f"validate-fun: {validate_stat}")
        print(f"vote-fun: {vote_stat}")
        print(f"count-vote-fun: {count_vote_stat}")
        print(f"display-fun: {display_stat}")

        with open(out_file, "w") as f:
            f.write(f"# time spent (micro-seconds)\n")
            f.write(f"{'# function name':<25} {'min':<25} {'max':<25} {'mean':<25} {'median':<25} {'std':<25}\n")
            
            latency_stat_writer(f, validate_stat, "validate-fun")
            latency_stat_writer(f, vote_stat, "vote-fun")
            latency_stat_writer(f, count_vote_stat, "count-vote-fun")
            latency_stat_writer(f, display_stat, "display-fun")

parse_trace_file('baseline_10rps_10m.csv', 'baseline.data')
parse_trace_file('both_10rps_10m.csv', 'both.data')
parse_trace_file('leader_10rps_10m.csv', 'leader.data')
parse_trace_file('member_10rps_10m.csv', 'member.data')
parse_trace_file('ego_10rps_10m.csv', 'ego.data')
parse_trace_file('rsa_10rps_10m.csv', 'rsa.data')
parse_trace_file('both_sig_10rps_10m.csv', 'both_sig.data')