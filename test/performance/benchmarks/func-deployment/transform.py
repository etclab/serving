#!/bin/python

import argparse
import csv
import statistics as stat

def main():
    parser = argparse.ArgumentParser()

    parser.add_argument(
        "data_file", type=str, help="path to data file"
    )
    
    parser.add_argument(
        "out_file", type=str, help="path to out file"
    )
    
    args = parser.parse_args()

    # .data file but has actually csv
    data_file = args.data_file
    with open(data_file, mode="r", newline='') as file:
        reader = csv.DictReader(file)
        
        all = []
        func_name = 'func-name'
        for row in reader:
            func_name = row['pod_name'].split('-')[0]
            
            ready_time = row['pod_ready_time']
            scheduled_time = row['pod_scheduled_time']
            
            diff = int(ready_time) - int(scheduled_time)
            
            all.append(diff)
        
        all.sort()
        # ignore the first and last five
        tall = all[5:-5]

        min_val = min(tall)
        max_val = max(tall)
        mean = stat.mean(tall)
        std = stat.stdev(tall)
        median = stat.median(tall)
        
        print(f"min: {min_val}, max: {max_val}, mean: {mean}, median: {median}, std: {std}")
        
        with open(args.out_file, "w") as f:
            f.write(f"# pod deployment time (seconds)\n")
            f.write(f"{'# function name':<25} {'min':<25} {'max':<25} {'mean':<25} {'median':<25} {'std':<25}\n")
            f.write(f"{f'{func_name}':<25} {min_val:<25} {max_val:<25} {mean:<25} {median:<25} {std:<25}\n")

if __name__ == "__main__":
    main()