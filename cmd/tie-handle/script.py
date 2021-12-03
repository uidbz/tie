#!/usr/bin/env python 
import argparse
parser = argparse.ArgumentParser()
parser.add_argument("-input")
parser.add_argument("-settings")
parser.add_argument("-output")
args = parser.parse_args()
print(args.output)

#import sys
#print ('Argument List:', str(sys.argv))

f = open(args.input,"r")
lines = f.readlines()

with open(args.output + '/result.txt', 'w') as f:
    f.write(str(int(lines[0])*2))
