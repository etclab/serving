#!/bin/python

import argparse
import os
import statistics as stat

# sample durations
# {'min': '224.813µs', 'mean': '35.656s', '50': '24.735s', '90': '1m34s', '95': '1m43s', '99': '2m9s', 'max': '3m50s'}
# converts all to milliseconds
def parse_duration(duration):
    if duration.endswith('ms'):
        return float(duration[:-2])
    elif duration.endswith('µs'):
        return float(duration[:-2]) / 1000
    elif duration.endswith('s'):
        if 'm' in duration:
            # handle minutes
            parts = duration.split('m')
            minutes = float(parts[0])
            seconds = float(parts[1][:-1])
            return (minutes * 60 + seconds) * 1000
        else:
            return float(duration[:-1]) * 1000
    else:
        return float(0)
    

def main():
    parser = argparse.ArgumentParser()

    parser.add_argument(
        "log_folder", type=str, help="path to log files"
    )
    
    parser.add_argument(
        "out_file", type=str, help="path to out file"
    )
    
    parser.add_argument(
        "label", type=str, help="data label for the output"
    )
    
    args = parser.parse_args()

    log_folder = args.log_folder
    all_entires = os.listdir(log_folder)
    
    data = {}
    search_string = "Latencies"
    
    for entry in all_entires:
        file_path =  os.path.join(log_folder, entry)
        name_only = entry.split('.')[0]
        
        data[name_only] = {}
        
        print(f"Processing {entry}...")
        
        match = ''
        with open(file_path, 'r') as file:
            for line in file:
                if search_string in line:
                    match = line.strip()
           
            # match looks like:
            # Latencies     [min, mean, 50, 90, 95, 99, max]  12.304ms, 20.65ms, 19.36ms, 26.385ms, 28.953ms, 45.551ms, 30.001s
            if match: 
                left = match.find('[')
                right = match.find(']')
                
                labels = [e.strip() for e in match[left+1:right].split(',')]
                values = [e.strip() for e in match[right+1:].split(',')]
    
                store = data[name_only]
                for i, label in enumerate(labels):
                    store[label] = values[i]
    
    data_ms = {}
    for entry in data:
        data_ms[entry] = {}
        for key in data[entry]:
            data_ms[entry][key] = parse_duration(data[entry][key])
            
            
    with open(args.out_file, "w") as f:
        f.write(f"# func-invocation time (ms)\n")
        f.write(f"#{f'{args.label}':<25} {'p50':<25} {'p90':<25} {'min':<25} {'max':<25} {'mean':<25}\n")
        
        rates = sorted([int(rate) for rate in list(data_ms.keys())])
        for rate in rates:
            row = data_ms[f'{rate}']
            f.write(f"{f'{rate}':<25} {row['50']:<25} {row['90']:<25} {row['min']:<25} {row['max']:<25} {row['mean']:<25}\n")

if __name__ == "__main__":
    main()