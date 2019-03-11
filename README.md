# rtldavis

## rtldavis

### About this Repository

This repository is a fork of [https://github.com/bemasher/rtldavis](https://github.com/bemasher/rtldavis) for use with custom receiver modules. It has been modified in numerous ways.
1) Added EU frequencies
2) Handling of more than one concurrent transmitters.
3) Output format is changed for use with the weewx-rtldavis driver which does the data parsing. 


## Installation

#### Install packages needed for rtldavis

sudo apt-get install golang git cmake librtlsdr-dev

#### Setup Udev Rules

Next, you need to add some udev rules to make the dongle available for the non-root users. First you want to find the vendor id and product id for your dongle.
The way I did this was to run:

lsusb

The last line was the Realtek dongle:
    Bus 001 Device 008: ID 0bda:2838 Realtek Semiconductor Corp.
    Bus 001 Device 005: ID 0bda:2838 Realtek Semiconductor Corp. RTL2838 DVB-T

The important parts are "0bda" (the vendor id) and "2838" (the product id).

Create a new file as root named /etc/udev/rules.d/20.rtlsdr.rules that contains the following line:
    nano /etc/udev/rules.d/20.rtlsdr.rules
    SUBSYSTEM=="usb", ATTRS{idVendor}=="0bda", ATTRS{idProduct}=="2838", GROUP="adm", MODE="0666", SYMLINK+="rtl_sdr"

With the vendor and product ids for your particular dongle. This should make the dongle accessible to any user in the adm group. and add a /dev/rtl_sdr symlink when the dongle is attached.

#### Get librtlsdr

    cd /home/pi
    git clone https://github.com/steve-m/librtlsdr.git
    cd librtlsdr
    mkdir build
    cd build
    cmake ../ -DINSTALL_UDEV_RULES=ON
    make
    sudo make install
    sudo ldconfig

#### Create ~/profile

    sudo nano ~/.profile
    export GOROOT=/usr/lib/go
    export GOPATH=$HOME/work
    export PATH=$PATH:$GOROOT/bin:$GOPATH/bin
    source ~/.profile

#### Get the rtldavis package

    cd /home/pi
    go get -v github.com/lheijst/rtldavis

#### Compiling GO sources

    cd $GOPATH/src/github.com/lheijst/rtldavis
    git submodule init
    git submodule update
    go install -v .

#### Start program rtldavis

    $GOPATH/bin/rtldavis

#### Usage

Available command-line flags are as follows:

```
Usage of rtldavis:
  -tr [transmitters]
    	code of the stations to listen for: tr1=1 tr2=2 tr3=4 tr4=8 tr5=16 tr6=32 tr7=64 tr8=128
        Default = 1

  -tf [tranceiver frequencies]
        EU or US
        Default = EU

  -ex [extra loop_delay in ms]
        In case a lot of messages are missed we might try to use the -ex parameter, like -ex 200
        Default = -ex 0
```

### License

The source of this project is licensed under GPL v3.0. According to [http://choosealicense.com/licenses/gpl-3.0/](http://choosealicense.com/licenses/gpl-3.0/) you may:

#### Required:

 * **Disclose Source:** Source code must be made available when distributing the software. In the case of LGPL and OSL 3.0, the source for the library (and not the entire program) must be made available.
 * **License and copyright notice:** Include a copy of the license and copyright notice with the code.
 * **State Changes:** Indicate significant changes made to the code.

#### Permitted:

 * **Commercial Use:** This software and derivatives may be used for commercial purposes.
 * **Distribution:** You may distribute this software.
 * **Modification:** This software may be modified.
 * **Patent Use:** This license provides an express grant of patent rights from the contributor to the recipient.
 * **Private Use:** You may use and modify the software without distributing it.

#### Forbidden:

 * **Hold Liable:** Software is provided without warranty and the software author\/license owner cannot be held liable for damages.

### Feedback
If you have any general questions or feedback leave a comment below. For bugs, feature suggestions and anything directly relating to the program itself, submit an issue.
