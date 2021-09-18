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

with open(args.output + '/readme.txt', 'w') as f:
    f.write('readme')
