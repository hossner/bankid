var bidSocket
var sessID = ""

function loaded(msg){
    console.log(msg);
    bidSocket = new WebSocket("ws://localhost:8080/ws")
    var long = document.getElementById("long");
    var lat = document.getElementById("lat");

    var update = function(){
        bidSocket.onmessage = function (event) {
            msg = JSON.parse(event.data)
            console.log(msg.action)
            console.log(msg.value)
        //   var longlatArr = event.data.split(" ");
        //   long.textContent = longlatArr[1].toString();
        //   lat.textContent = longlatArr[0].toString();
        }
      };
      window.setTimeout(update);

}

function sendPnr(nr){
    console.log(nr)
    bidSocket.send(JSON.stringify({action:"pnrAuth", value:nr, id:sessID}))
}